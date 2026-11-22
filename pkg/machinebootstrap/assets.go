package machinebootstrap

import _ "embed"

// p2pInstallBody is the canonical host p2p setup script. The p2p-static chart
// serves it at /p2p-install.sh; the embedded copy is retained for legacy
// inline-block rendering and migration tests.
//
//go:embed assets/p2p-install.sh
var p2pInstallBody string
