// The stock atryum binary. All CLI and server logic lives in pkg/atryum so
// downstream programs can embed atryum and extend it; this package only
// contributes the third-party notices bundle (licenses_stub.go in dev builds,
// the generated licenses_gen.go under the release_notices tag).
package main

import "github.com/validmind/atryum/pkg/atryum"

func main() {
	atryum.Main(atryum.WithThirdPartyNotices(thirdPartyNotices))
}
