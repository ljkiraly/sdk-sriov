// Copyright (c) 2020-2023 Doc.ai and/or its affiliates.
//
// Copyright (c) 2021-2023 Nordix Foundation.
//
// Copyright (c) 2023 Cisco and/or its affiliates.
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

// Package multitoken provides chain elements for inserting SRIOV tokens into request and response
package multitoken

import (
	"context"
	"os"
	"strings"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/common"
	"github.com/ljkiraly/sdk/pkg/networkservice/core/next"

	"github.com/ljkiraly/sdk-sriov/pkg/tools/tokens"
)

const (
	sriovTokenLabel    = "sriovToken"
	serviceDomainLabel = "serviceDomain"
)

type tokenClient struct {
	config tokenConfig
}

// NewClient returns a new token client chain element
func NewClient() networkservice.NetworkServiceClient {
	return &tokenClient{
		createTokenElement(tokens.FromEnv(os.Environ())),
	}
}

func (c *tokenClient) Request(ctx context.Context, request *networkservice.NetworkServiceRequest, opts ...grpc.CallOption) (*networkservice.Connection, error) {
	isEstablished := c.config.get(request.GetConnection()) != ""

	var tokenID string
	var tokenName string
	if labels := request.GetConnection().GetLabels(); labels != nil {
		var ok bool
		if tokenName, ok = labels[sriovTokenLabel]; ok {
			tokenID = c.config.assign(tokenName, request.GetConnection())
			if tokenID == "" {
				return nil, errors.Errorf("no free token for the name: %v", tokenName)
			}

			request = request.Clone()
			delete(request.GetConnection().GetLabels(), sriovTokenLabel)
			request.GetConnection().GetLabels()[serviceDomainLabel] = strings.Split(tokenName, "/")[0]

			for _, mech := range request.GetMechanismPreferences() {
				if mech.Parameters == nil {
					mech.Parameters = map[string]string{}
				}
				mech.Parameters[common.DeviceTokenIDKey] = tokenID
			}
		}
	}

	conn, err := next.Client(ctx).Request(ctx, request, opts...)
	if err != nil && tokenID != "" && !isEstablished {
		c.config.release(request.GetConnection())
	}

	if err == nil && tokenName != "" {
		// Set the previous values in the labels. We need them for healing
		delete(conn.GetLabels(), serviceDomainLabel)
		conn.GetLabels()[sriovTokenLabel] = tokenName
	}

	return conn, err
}

func (c *tokenClient) Close(ctx context.Context, conn *networkservice.Connection, opts ...grpc.CallOption) (*empty.Empty, error) {
	c.config.release(conn)
	return next.Client(ctx).Close(ctx, conn, opts...)
}
