package migrations

// migration037 stores which kind of gateway (truefoundry, generic) asserted a
// call's identity, so it reads back with the rest of the asserted identity
// instead of appearing only on the changes made before the first read.
func migration037() Definition {
	return Definition{
		Version: 37,
		Name:    "037_invocations_asserted_vendor",
		Steps: []Step{
			AddColumnIfMissing("invocations", "asserted_vendor", "TEXT", "TEXT"),
		},
	}
}
