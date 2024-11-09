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

package eras

import (
	"github.com/blinklabs-io/gouroboros/ledger/byron"
	"github.com/blinklabs-io/node/config/cardano"
)

var ByronEraDesc = EraDesc{
	Id:              byron.EraIdByron,
	Name:            byron.EraNameByron,
	EpochLengthFunc: EpochLengthByron,
}

func EpochLengthByron(nodeConfig *cardano.CardanoNodeConfig) (uint, uint, error) {
	byronGenesis, err := nodeConfig.ByronGenesis()
	if err != nil {
		return 0, 0, err
	}
	return uint(byronGenesis.BlockVersionData.SlotDuration),
		uint(byronGenesis.ProtocolConsts.K * 10),
		nil
}