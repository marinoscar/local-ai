package doctor

const (
	CAT001 = "LAI-CAT-001"
	CAT010 = "LAI-CAT-010"
)

var table = []Code{
	{
		ID:       CAT001,
		Severity: Error,
		Title:    Msg{EN: "Catalog file cannot be parsed", ES: "No se puede analizar el archivo del catálogo"},
		Fix:      Msg{EN: "Restore it", ES: "Restáurelo"},
		Action:   "ark.import",
	},
	{
		ID:       CAT010,
		Severity: Warning,
		Title:    Msg{EN: "Lint: schema problem", ES: "Lint: problema de esquema"},
		Fix:      Msg{EN: "Fix the YAML", ES: "Corrija el YAML"},
	},
}
