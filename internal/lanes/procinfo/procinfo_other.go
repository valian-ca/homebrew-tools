//go:build !(darwin && cgo)

package procinfo

type system struct{}

func System() Source { return system{} }

func (system) Snapshot() ([]Proc, error) { return nil, ErrUnsupported }

func (system) ListeningPID(int) (int, bool) { return 0, false }
