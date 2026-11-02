package doctor

const (
	CAT001 = "LAI-CAT-001"
	CAT001dup = "LAI-CAT-001"
	CAT077 = "LAI-CAT-077"
)

var table = []Code{
	{
		ID:       CAT001,
		Severity: Error,
		Title:    Msg{EN: "x", ES: "x"},
		Fix:      Msg{EN: "x", ES: "x"},
	},
	{
		ID:       CAT001dup,
		Severity: Error,
		Title:    Msg{EN: "dup", ES: "dup"},
		Fix:      Msg{EN: "dup", ES: "dup"},
	},
	{
		ID:       CAT077,
		Severity: Warning,
		Title:    Msg{EN: "extra", ES: "extra"},
		Fix:      Msg{EN: "extra", ES: "extra"},
	},
}
