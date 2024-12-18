// Copyright 2024 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dingo

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"syscall"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/protocol/blockfetch"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	"github.com/blinklabs-io/gouroboros/protocol/peersharing"
	"github.com/blinklabs-io/gouroboros/protocol/txsubmission"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sys/unix"
)

const (
	initialReconnectDelay  = 1 * time.Second
	maxReconnectDelay      = 128 * time.Second
	reconnectBackoffFactor = 2
)

type outboundPeer struct {
	Address        string
	ReconnectCount int
	ReconnectDelay time.Duration
	sharable       bool
}

func (n *Node) startOutboundConnections() {
	n.config.logger.Debug(
		"starting connections",
		"component", "network",
		"role", "client",
	)
	var tmpPeers []outboundPeer
	for _, host := range n.config.topologyConfig.BootstrapPeers {
		n.config.logger.Debug(
			fmt.Sprintf(
				"adding bootstrap peer topology host: %s:%d",
				host.Address,
				host.Port,
			),
			"component", "network",
			"role", "client",
		)
		tmpPeers = append(
			tmpPeers,
			outboundPeer{
				Address: net.JoinHostPort(
					host.Address,
					strconv.Itoa(int(host.Port)),
				),
			},
		)
	}
	for _, localRoot := range n.config.topologyConfig.LocalRoots {
		for _, host := range localRoot.AccessPoints {
			n.config.logger.Debug(
				fmt.Sprintf(
					"adding localRoot topology host: %s:%d",
					host.Address,
					host.Port,
				),
				"component", "network",
				"role", "client",
			)
			tmpPeers = append(
				tmpPeers,
				outboundPeer{
					Address: net.JoinHostPort(
						host.Address,
						strconv.Itoa(int(host.Port)),
					),
					sharable: localRoot.Advertise,
				},
			)
		}
	}
	for _, publicRoot := range n.config.topologyConfig.PublicRoots {
		for _, host := range publicRoot.AccessPoints {
			n.config.logger.Debug(
				fmt.Sprintf(
					"adding publicRoot topology host: %s:%d",
					host.Address,
					host.Port,
				),
				"component", "network",
				"role", "client",
			)
			tmpPeers = append(
				tmpPeers,
				outboundPeer{
					Address: net.JoinHostPort(
						host.Address,
						strconv.Itoa(int(host.Port)),
					),
					sharable: publicRoot.Advertise,
				},
			)
		}
	}
	// Start outbound connections
	for _, tmpPeer := range tmpPeers {
		go func(peer outboundPeer) {
			if err := n.createOutboundConnection(peer); err != nil {
				n.config.logger.Error(
					fmt.Sprintf(
						"outbound: failed to establish connection to %s: %s",
						peer.Address,
						err,
					),
					"component", "network",
				)
				go n.reconnectOutboundConnection(peer)
			}
		}(tmpPeer)
	}

}

func (n *Node) createOutboundConnection(peer outboundPeer) error {
	t := otel.Tracer("")
	if t != nil {
		_, span := t.Start(context.TODO(), "create outbound connection")
		defer span.End()
		span.SetAttributes(
			attribute.String("peer.address", peer.Address),
		)
	}

	var clientAddr net.Addr
	dialer := net.Dialer{
		Timeout: 10 * time.Second,
	}
	if n.config.outboundSourcePort > 0 {
		// Setup connection to use our listening port as the source port
		// This is required for peer sharing to be useful
		clientAddr, _ = net.ResolveTCPAddr(
			"tcp",
			fmt.Sprintf(":%d", n.config.outboundSourcePort),
		)
		dialer.LocalAddr = clientAddr
		dialer.Control = outboundSocketControl
	}
	n.config.logger.Debug(
		fmt.Sprintf(
			"establishing TCP connection to: %s",
			peer.Address,
		),
		"component", "network",
		"role", "client",
	)
	tmpConn, err := dialer.Dial("tcp", peer.Address)
	if err != nil {
		return err
	}
	// Build connection options
	connOpts := []ouroboros.ConnectionOptionFunc{
		ouroboros.WithConnection(tmpConn),
		ouroboros.WithLogger(n.config.logger),
		ouroboros.WithNetworkMagic(n.config.networkMagic),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithKeepAlive(true),
		ouroboros.WithFullDuplex(true),
		ouroboros.WithPeerSharing(n.config.peerSharing),
		ouroboros.WithPeerSharingConfig(
			peersharing.NewConfig(
				slices.Concat(
					n.peersharingClientConnOpts(),
					n.peersharingServerConnOpts(),
				)...,
			),
		),
		ouroboros.WithTxSubmissionConfig(
			txsubmission.NewConfig(
				slices.Concat(
					n.txsubmissionClientConnOpts(),
					n.txsubmissionServerConnOpts(),
				)...,
			),
		),
		ouroboros.WithChainSyncConfig(
			chainsync.NewConfig(
				slices.Concat(
					n.chainsyncClientConnOpts(),
					n.chainsyncServerConnOpts(),
				)...,
			),
		),
		ouroboros.WithBlockFetchConfig(
			blockfetch.NewConfig(
				slices.Concat(
					n.blockfetchClientConnOpts(),
					n.blockfetchServerConnOpts(),
				)...,
			),
		),
	}
	// Setup Ouroboros connection
	n.config.logger.Debug(
		fmt.Sprintf(
			"establishing ouroboros protocol to %s",
			peer.Address,
		),
		"component", "network",
		"role", "client",
	)
	oConn, err := ouroboros.NewConnection(
		connOpts...,
	)
	if err != nil {
		return err
	}
	n.config.logger.Info(
		fmt.Sprintf("connected ouroboros to %s", peer.Address),
		"component", "network",
		"role", "client",
	)
	n.config.logger.Debug(
		fmt.Sprintf("peer address mapping: address: %s", peer.Address),
		"component", "network",
		"role", "client",
		"connection_id", oConn.Id().String(),
	)
	peer.ReconnectCount = 0
	peer.ReconnectDelay = 0
	// Add to connection manager
	n.connManager.AddConnection(oConn)
	// Add to outbound connection tracking
	n.outboundConnsMutex.Lock()
	n.outboundConns[oConn.Id()] = peer
	n.outboundConnsMutex.Unlock()
	// TODO: replace this with handling for multiple chainsync clients
	// Start chainsync client if we don't have another
	n.chainsyncState.Lock()
	defer n.chainsyncState.Unlock()
	chainsyncClientConnId := n.chainsyncState.GetClientConnId()
	if chainsyncClientConnId == nil {
		if err := n.chainsyncClientStart(oConn.Id()); err != nil {
			return err
		}
		n.chainsyncState.SetClientConnId(oConn.Id())
	}
	// Start txsubmission client
	if err := n.txsubmissionClientStart(oConn.Id()); err != nil {
		return err
	}
	return nil
}

func (n *Node) reconnectOutboundConnection(peer outboundPeer) {
	for {
		if peer.ReconnectDelay == 0 {
			peer.ReconnectDelay = initialReconnectDelay
		} else if peer.ReconnectDelay < maxReconnectDelay {
			peer.ReconnectDelay = peer.ReconnectDelay * reconnectBackoffFactor
		}
		peer.ReconnectCount += 1
		n.config.logger.Info(
			fmt.Sprintf(
				"outbound: delaying %s (retry %d) before reconnecting to %s",
				peer.ReconnectDelay,
				peer.ReconnectCount,
				peer.Address,
			),
			"component", "network",
		)
		time.Sleep(peer.ReconnectDelay)
		if err := n.createOutboundConnection(peer); err != nil {
			n.config.logger.Error(
				fmt.Sprintf(
					"outbound: failed to establish connection to %s: %s",
					peer.Address,
					err,
				),
				"component", "network",
			)
			continue
		}
		peer.ReconnectCount = 0
		return
	}
}

// outboundSocketControl is a helper function for setting socket options outbound sockets
func outboundSocketControl(network, address string, c syscall.RawConn) error {
	var innerErr error
	err := c.Control(func(fd uintptr) {
		err := unix.SetsockoptInt(
			int(fd),
			unix.SOL_SOCKET,
			unix.SO_REUSEADDR,
			1,
		)
		if err != nil {
			innerErr = err
			return
		}
		err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		if err != nil {
			innerErr = err
			return
		}
	})
	if innerErr != nil {
		return innerErr
	}
	if err != nil {
		return err
	}
	return nil
}
