// +build !js

package webrtc

import (
	"context"
	"errors"
	"sync"

	"github.com/pion/ice"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v2/internal/mux"
)

// ICETransport allows an application access to information about the ICE
// transport over which packets are sent and received.
type ICETransport struct {
	lock sync.RWMutex

	role ICERole
	// Component ICEComponent
	// State ICETransportState
	// gatheringState ICEGathererState

	onConnectionStateChangeHdlr       func(ICETransportState)
	onSelectedCandidatePairChangeHdlr func(*ICECandidatePair)

	state ICETransportState

	gatherer *ICEGatherer
	conn     *ice.Conn
	mux      *mux.Mux

	api *API

	log logging.LeveledLogger
}

// func (t *ICETransport) GetLocalCandidates() []ICECandidate {
//
// }
//
// func (t *ICETransport) GetRemoteCandidates() []ICECandidate {
//
// }
//
// func (t *ICETransport) GetSelectedCandidatePair() ICECandidatePair {
//
// }
//
// func (t *ICETransport) GetLocalParameters() ICEParameters {
//
// }
//
// func (t *ICETransport) GetRemoteParameters() ICEParameters {
//
// }

// NewICETransport creates a new NewICETransport.
// This constructor is part of the ORTC API. It is not
// meant to be used together with the basic WebRTC API.
func (api *API) NewICETransport(gatherer *ICEGatherer) *ICETransport {
	return &ICETransport{
		gatherer: gatherer,
		api:      api,
		log:      api.settingEngine.LoggerFactory.NewLogger("ortc"),
		state:    ICETransportStateNew,
	}
}

// Start incoming connectivity checks based on its configured role.
func (t *ICETransport) Start(gatherer *ICEGatherer, params ICEParameters, role *ICERole) error {
	t.lock.Lock()
	defer t.lock.Unlock()

	if gatherer != nil {
		t.gatherer = gatherer
	}

	if err := t.ensureGatherer(); err != nil {
		return err
	}

	agent := t.gatherer.agent
	if err := agent.OnConnectionStateChange(func(iceState ice.ConnectionState) {
		state := newICETransportStateFromICE(iceState)
		t.lock.Lock()
		t.state = state
		t.lock.Unlock()

		t.onConnectionStateChange(state)
	}); err != nil {
		return err
	}
	if err := agent.OnSelectedCandidatePairChange(func(local, remote *ice.Candidate) {
		candidates, err := newICECandidatesFromICE([]*ice.Candidate{local, remote})
		if err != nil {
			t.log.Warnf("Unable to convert ICE candidates to ICECandidates: %s", err)
			return
		}
		t.onSelectedCandidatePairChange(NewICECandidatePair(&candidates[0], &candidates[1]))
	}); err != nil {
		return err
	}

	if role == nil {
		controlled := ICERoleControlled
		role = &controlled
	}
	t.role = *role

	// Drop the lock here to allow trickle-ICE candidates to be
	// added so that the agent can complete a connection
	t.lock.Unlock()

	var iceConn *ice.Conn
	var err error
	switch *role {
	case ICERoleControlling:
		iceConn, err = agent.Dial(context.TODO(),
			params.UsernameFragment,
			params.Password)

	case ICERoleControlled:
		iceConn, err = agent.Accept(context.TODO(),
			params.UsernameFragment,
			params.Password)

	default:
		err = errors.New("unknown ICE Role")
	}

	// Reacquire the lock to set the connection/mux
	t.lock.Lock()
	if err != nil {
		return err
	}

	t.conn = iceConn

	config := mux.Config{
		Conn:          t.conn,
		BufferSize:    receiveMTU,
		LoggerFactory: t.api.settingEngine.LoggerFactory,
	}
	t.mux = mux.NewMux(config)

	return nil
}

// Stop irreversibly stops the ICETransport.
func (t *ICETransport) Stop() error {
	t.lock.Lock()
	defer t.lock.Unlock()

	if t.mux != nil {
		return t.mux.Close()
	} else if t.gatherer != nil {
		return t.gatherer.Close()
	}
	return nil
}

// OnSelectedCandidatePairChange sets a handler that is invoked when a new
// ICE candidate pair is selected
func (t *ICETransport) OnSelectedCandidatePairChange(f func(*ICECandidatePair)) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.onSelectedCandidatePairChangeHdlr = f
}

func (t *ICETransport) onSelectedCandidatePairChange(pair *ICECandidatePair) {
	t.lock.RLock()
	hdlr := t.onSelectedCandidatePairChangeHdlr
	t.lock.RUnlock()
	if hdlr != nil {
		hdlr(pair)
	}
}

// OnConnectionStateChange sets a handler that is fired when the ICE
// connection state changes.
func (t *ICETransport) OnConnectionStateChange(f func(ICETransportState)) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.onConnectionStateChangeHdlr = f
}

func (t *ICETransport) onConnectionStateChange(state ICETransportState) {
	t.lock.RLock()
	hdlr := t.onConnectionStateChangeHdlr
	t.lock.RUnlock()
	if hdlr != nil {
		hdlr(state)
	}
}

// Role indicates the current role of the ICE transport.
func (t *ICETransport) Role() ICERole {
	t.lock.RLock()
	defer t.lock.RUnlock()

	return t.role
}

// SetRemoteCandidates sets the sequence of candidates associated with the remote ICETransport.
func (t *ICETransport) SetRemoteCandidates(remoteCandidates []ICECandidate) error {
	t.lock.RLock()
	defer t.lock.RUnlock()

	if err := t.ensureGatherer(); err != nil {
		return err
	}

	for _, c := range remoteCandidates {
		i, err := c.toICE()
		if err != nil {
			return err
		}
		err = t.gatherer.agent.AddRemoteCandidate(i)
		if err != nil {
			return err
		}
	}

	return nil
}

// AddRemoteCandidate adds a candidate associated with the remote ICETransport.
func (t *ICETransport) AddRemoteCandidate(remoteCandidate ICECandidate) error {
	t.lock.RLock()
	defer t.lock.RUnlock()

	if err := t.ensureGatherer(); err != nil {
		return err
	}

	c, err := remoteCandidate.toICE()
	if err != nil {
		return err
	}
	err = t.gatherer.agent.AddRemoteCandidate(c)
	if err != nil {
		return err
	}

	return nil
}

// State returns the current ice transport state.
func (t *ICETransport) State() ICETransportState {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.state
}

// NewEndpoint registers a new endpoint on the underlying mux.
func (t *ICETransport) NewEndpoint(f mux.MatchFunc) *mux.Endpoint {
	t.lock.Lock()
	defer t.lock.Unlock()
	return t.mux.NewEndpoint(f)
}

func (t *ICETransport) ensureGatherer() error {
	if t.gatherer == nil ||
		t.gatherer.agent == nil {
		return errors.New("gatherer not started")
	}

	return nil
}
