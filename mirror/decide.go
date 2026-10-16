package mirror

// Action is what one path needs in a pass.
type Action int

const (
	Same         Action = iota // both sides hold the same content (or neither holds it)
	Download                   // the cloud changed (or added) it: bring it here
	Upload                     // it changed (or was added) here: send it up
	DeleteLocal                // deleted in the cloud, unchanged here: move to the trash
	DeleteRemote               // deleted here, unchanged in the cloud: remove from the vault
	Conflict                   // changed on both sides: keep both
)

func (a Action) String() string {
	return [...]string{"same", "download", "upload", "delete-local", "delete-remote", "conflict"}[a]
}

// decide is the three-way merge of one path: base = the content both
// sides had at the last sync, local and remote = what each side holds
// now ("" = absent). The rules are vault.js's:
//   - only one side changed: that side wins, deletions included;
//   - deleted on one side, changed on the other: the change wins
//     (never a silent loss of an edit);
//   - changed differently on both: local keeps the path (VD-4) and the
//     cloud's version is kept beside it as a conflict copy (FILES-V);
//   - no base (first sync of a folder, or a path new on both sides):
//     nothing is ever deleted, both contents survive.
func decide(base, local, remote string) Action {
	switch {
	case local == remote:
		return Same
	case base != "" && local == base:
		if remote == "" {
			return DeleteLocal
		}
		return Download
	case base != "" && remote == base:
		if local == "" {
			return DeleteRemote
		}
		return Upload
	case local == "":
		return Download
	case remote == "":
		return Upload
	default:
		return Conflict
	}
}
