package i18n

// Keys of the output layer. Spanish keeps the same formatting verbs in the same order (the table
// test checks it).
const (
	// KeyFixLabel prefixes the fix line of a problem ("→ Fix:" is built from the arrow and this).
	KeyFixLabel Key = "label.fix"
	// KeyWaitLabel prefixes the wait line of a rate-limited problem.
	KeyWaitLabel Key = "label.wait"
	// KeyTriesLeft is the line after a wrong PIN: how many tries remain (%d).
	KeyTriesLeft Key = "pin.tries_left"
	// KeyPromptPIN is the prompt for the admin PIN (no trailing newline).
	KeyPromptPIN Key = "pin.prompt"
	// KeyNoPIN is the message when nothing was typed or piped in as the PIN.
	KeyNoPIN Key = "pin.none_read"
	// KeyConfirmSuffix follows a yes/no question (default No).
	KeyConfirmSuffix Key = "confirm.suffix"
	// KeySecretWarning is printed by PrintSecretWarning.
	KeySecretWarning Key = "secret.warning"

	// KeyJustNow, KeySoon, KeyAgo and KeyIn build relative times ("12 hours ago", "in 3 minutes").
	KeyJustNow Key = "time.just_now"
	KeySoon    Key = "time.soon"
	KeyAgo     Key = "time.ago" // %d %s
	KeyIn      Key = "time.in"  // %d %s

	// Unit words for relative times: key + ".one" / ".other".
	KeyUnitSecond Key = "unit.second"
	KeyUnitMinute Key = "unit.minute"
	KeyUnitHour   Key = "unit.hour"
	KeyUnitDay    Key = "unit.day"
	KeyUnitMonth  Key = "unit.month"
	KeyUnitYear   Key = "unit.year"
)

var en = map[Key]string{
	KeyFixLabel:      "Fix:",
	KeyWaitLabel:     "Wait:",
	KeyTriesLeft:     "Tries left: %d.",
	KeyPromptPIN:     "Admin PIN: ",
	KeyNoPIN:         "No PIN was entered.",
	KeyConfirmSuffix: " [y/N] ",
	KeySecretWarning: "This is the secret key for this node. Do not paste it in chats or emails.",

	KeyJustNow: "just now",
	KeySoon:    "in a moment",
	KeyAgo:     "%d %s ago",
	KeyIn:      "in %d %s",

	KeyUnitSecond + ".one":   "second",
	KeyUnitSecond + ".other": "seconds",
	KeyUnitMinute + ".one":   "minute",
	KeyUnitMinute + ".other": "minutes",
	KeyUnitHour + ".one":     "hour",
	KeyUnitHour + ".other":   "hours",
	KeyUnitDay + ".one":      "day",
	KeyUnitDay + ".other":    "days",
	KeyUnitMonth + ".one":    "month",
	KeyUnitMonth + ".other":  "months",
	KeyUnitYear + ".one":     "year",
	KeyUnitYear + ".other":   "years",
}
