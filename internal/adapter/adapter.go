// Package adapter composes the vrclog-go Engine from the built-in VRChat
// core adapter and the compile-time community adapters provided by
// vrclog-adapters.
package adapter

import (
	vrclog "github.com/vrclog/vrclog-go"

	"github.com/vrclog/vrclog-adapters/iwasync3"
	"github.com/vrclog/vrclog-adapters/yamaplayer"
)

// LoadedAdapter describes one Adapter composed into the Engine, for
// reporting via health/adapters endpoints.
type LoadedAdapter struct {
	ID     string `json:"id"`
	Origin string `json:"origin"` // "core" or "community"
}

// BuildEngine composes the built-in VRChat core adapter with all
// community adapters and constructs the vrclog Engine. The core adapter
// is always registered first; community adapters are listed explicitly
// (vrclog-adapters no longer exposes an aggregate All()) in the same
// order the old All() returned them.
func BuildEngine() (*vrclog.Engine, []LoadedAdapter, error) {
	core := vrclog.NewVRChatAdapter()
	community := []vrclog.Adapter{
		yamaplayer.New(),
		iwasync3.New(),
	}

	all := make([]vrclog.Adapter, 0, 1+len(community))
	all = append(all, core)
	all = append(all, community...)

	engine, err := vrclog.NewEngine(all...)
	if err != nil {
		return nil, nil, err
	}

	loaded := make([]LoadedAdapter, 0, len(all))
	loaded = append(loaded, LoadedAdapter{ID: string(core.ID()), Origin: "core"})
	for _, a := range community {
		loaded = append(loaded, LoadedAdapter{ID: string(a.ID()), Origin: "community"})
	}

	return engine, loaded, nil
}
