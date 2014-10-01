// Copyright (C) 2014 Jakob Borg and Contributors (see the CONTRIBUTORS file).
// All rights reserved. Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package discover

import (
	"log"
	"os"
	"strings"

	"github.com/syncthing/syncthing/logger"
)

var (
	debug     = strings.Contains(os.Getenv("STTRACE"), "discover") || os.Getenv("STTRACE") == "all"
	l         = logger.DefaultLogger
	logPrefix = logger.LogPrefix
)

func SetPrefix(prefix string) {
	logPrefix = prefix
}

func SetLogger(val *log.Logger) {
	l.Set(val)
}
