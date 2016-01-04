package types

type Plugin struct {
	Name       string // FIXME: probably not good idea
	Entrypoint []string
	Env        []string
	Cwd        string
	Privileged bool
	CapsAdd    []string
	CapsDrop   []string
}
