//go:build !darwin || !cgo

package main

import (
	"github.com/tangoui/tango"
	"github.com/tangoui/tango/ipc"
)

func alignMainTitleBar(*tango.App, *ipc.Router, *tango.Surface) {}
