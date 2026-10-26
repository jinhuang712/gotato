// Package lookup is what the bot's [script:lookup] says and what the cluster driver expects it to say, so the
// two cannot drift: the line and the total of one order in the export that testkit.BashLookupExport returns.
package lookup

import (
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/testkit"
)

// Answer is the sentence the scripted model gives once grep found the line and jq the total.
func Answer(line, total string) string {
	return fmt.Sprintf("%s is on line %s of the export, and its total is %s.", testkit.BashLookupOrder, line, total)
}

// Expected is the sentence a correct lookup ends with, worked out from the export itself.
func Expected() string {
	for i, l := range strings.Split(testkit.BashLookupExportBody(), "\n") {
		if strings.Contains(l, `"id": "`+testkit.BashLookupOrder+`"`) {
			return Answer(fmt.Sprint(i+1), testkit.BashLookupTotal())
		}
	}
	return ""
}
