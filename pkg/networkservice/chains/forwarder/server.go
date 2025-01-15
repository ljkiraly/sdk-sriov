// Copyright (c) 2020-2023 Doc.ai and/or its affiliates.
//
// Copyright (c) 2021-2023 Nordix Foundation.
//
// Copyright (c) 2022-2023 Cisco and/or its affiliates.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux
// +build linux

// Package xconnectns provides an Endpoint implementing the SR-IOV Forwarder networks service
package xconnectns

import (
	"context"
	"net/url"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/kernel"
	noopmech "github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/noop"
	vfiomech "github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/vfio"
	"github.com/ljkiraly/sdk-kernel/pkg/kernel/networkservice/connectioncontextkernel"
	"github.com/ljkiraly/sdk-kernel/pkg/kernel/networkservice/ethernetcontext"
	"github.com/ljkiraly/sdk-kernel/pkg/kernel/networkservice/inject"
	"github.com/ljkiraly/sdk/pkg/networkservice/chains/client"
	"github.com/ljkiraly/sdk/pkg/networkservice/chains/endpoint"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/connect"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/discover"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/filtermechanisms"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/mechanisms"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/mechanisms/recvfd"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/mechanismtranslation"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/null"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/roundrobin"
	"github.com/ljkiraly/sdk/pkg/networkservice/common/switchcase"
	"github.com/ljkiraly/sdk/pkg/networkservice/core/chain"
	"github.com/ljkiraly/sdk/pkg/tools/token"

	"github.com/ljkiraly/sdk-sriov/pkg/networkservice/common/mechanisms/noop"
	"github.com/ljkiraly/sdk-sriov/pkg/networkservice/common/mechanisms/vfio"
	"github.com/ljkiraly/sdk-sriov/pkg/networkservice/common/resetmechanism"
	"github.com/ljkiraly/sdk-sriov/pkg/networkservice/common/resourcepool"
	"github.com/ljkiraly/sdk-sriov/pkg/sriov"
	"github.com/ljkiraly/sdk-sriov/pkg/sriov/config"

	registryclient "github.com/ljkiraly/sdk/pkg/registry/chains/client"
	registryrecvfd "github.com/ljkiraly/sdk/pkg/registry/common/recvfd"
	registrysendfd "github.com/ljkiraly/sdk/pkg/registry/common/sendfd"
)

type sriovServer struct {
	endpoint.Endpoint
}

// NewServer - returns an Endpoint implementing the SR-IOV Forwarder networks service
//   - name - name of the Forwarder
//   - authzServer - policy for allowing or rejecting requests
//   - tokenGenerator - token.GeneratorFunc - generates tokens for use in Path
//   - pciPool - provides PCI functions
//   - resourcePool - provides SR-IOV resources
//   - sriovConfig - SR-IOV PCI functions config
//   - vfioDir - host /dev/vfio directory mount location
//   - cgroupBaseDir - host /sys/fs/cgroup/devices directory mount location
//   - clientUrl - *url.URL for the talking to the NSMgr
//   - ...clientDialOptions - dialOptions for dialing the NSMgr
func NewServer(
	ctx context.Context,
	name string,
	authzServer networkservice.NetworkServiceServer,
	authzMonitorConnectionServer networkservice.MonitorConnectionServer,
	tokenGenerator token.GeneratorFunc,
	pciPool resourcepool.PCIPool,
	resourcePool resourcepool.ResourcePool,
	sriovConfig *config.Config,
	vfioDir, cgroupBaseDir string,
	clientURL *url.URL,
	dialTimeout time.Duration,
	clientDialOptions ...grpc.DialOption,
) endpoint.Endpoint {
	nseClient := registryclient.NewNetworkServiceEndpointRegistryClient(ctx,
		registryclient.WithClientURL(clientURL),
		registryclient.WithNSEAdditionalFunctionality(
			registryrecvfd.NewNetworkServiceEndpointRegistryClient(),
			registrysendfd.NewNetworkServiceEndpointRegistryClient(),
		),
		registryclient.WithDialOptions(clientDialOptions...),
	)
	nsClient := registryclient.NewNetworkServiceRegistryClient(ctx,
		registryclient.WithClientURL(clientURL),
		registryclient.WithDialOptions(clientDialOptions...))

	rv := new(sriovServer)

	resourceLock := &sync.Mutex{}
	additionalFunctionality := []networkservice.NetworkServiceServer{
		recvfd.NewServer(),
		discover.NewServer(nsClient, nseClient),
		roundrobin.NewServer(),
		resetmechanism.NewServer(
			mechanisms.NewServer(map[string]networkservice.NetworkServiceServer{
				kernel.MECHANISM: chain.NewNetworkServiceServer(
					resourcepool.NewServer(sriov.KernelDriver, resourceLock, pciPool, resourcePool, sriovConfig),
				),
				vfiomech.MECHANISM: chain.NewNetworkServiceServer(
					resourcepool.NewServer(sriov.VFIOPCIDriver, resourceLock, pciPool, resourcePool, sriovConfig),
					vfio.NewServer(vfioDir, cgroupBaseDir),
				),
				noopmech.MECHANISM: null.NewServer(),
			}),
		),
		switchcase.NewServer(
			&switchcase.ServerCase{
				Condition: func(_ context.Context, conn *networkservice.Connection) bool {
					return conn.GetMechanism().GetType() != noopmech.MECHANISM
				},
				Server: chain.NewNetworkServiceServer(
					ethernetcontext.NewVFServer(),
					inject.NewServer(),
					connectioncontextkernel.NewServer(),
				),
			},
		),
		connect.NewServer(
			client.NewClient(
				ctx,
				client.WithName(name),
				client.WithAdditionalFunctionality(
					mechanismtranslation.NewClient(),
					noop.NewClient(),
					filtermechanisms.NewClient(),
				),
				client.WithDialTimeout(dialTimeout),
				client.WithDialOptions(clientDialOptions...),
				client.WithoutRefresh(),
			),
		),
	}

	rv.Endpoint = endpoint.NewServer(ctx, tokenGenerator,
		endpoint.WithName(name),
		endpoint.WithAuthorizeServer(authzServer),
		endpoint.WithAuthorizeMonitorConnectionServer(authzMonitorConnectionServer),
		endpoint.WithAdditionalFunctionality(additionalFunctionality...),
	)

	return rv
}
