package machinebootstrap

import _ "embed"

// p2pInstallBody is the single source of truth for the host p2p setup. It is
// inlined into the machine bootstrap Secret; the p2p-static chart does not
// serve it.
//
//go:embed assets/p2p-install.sh
var p2pInstallBody string
