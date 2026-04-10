package example

import "github.com/nextlevelbuilder/goclaw/internal/beta"

func init() {
	beta.Register(&ExampleFeature{})
}
