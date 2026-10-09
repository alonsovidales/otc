// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package agent

import (
	"errors"

	"github.com/alonsovidales/otc/bridge/fleet"
)

// The agent runs on the cluster's Linux servers only; these let the
// package build (and its parsers be tested) elsewhere.

var errLinuxOnly = errors.New("only on Linux")

func diskUsage(m Mount) (fleet.Disk, error) { return fleet.Disk{}, errLinuxOnly }

func clock() (*fleet.Clock, error) { return nil, errLinuxOnly }
