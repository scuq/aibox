// aibox — controlled, disposable AI development environments for Claude Code
// and Codex. See docs/PLAN.md for the design.
package main

import (
	"os"

	"github.com/scuq/aibox/internal/app"
)

func main() {
	os.Exit(app.Main(os.Args[1:]))
}
