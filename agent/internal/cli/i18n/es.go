package i18n

var es = map[Key]string{
	KeyFixLabel:      "Solución:",
	KeyWaitLabel:     "Espere:",
	KeyTriesLeft:     "Intentos restantes: %d.",
	KeyPromptPIN:     "PIN de administrador: ",
	KeyNoPIN:         "No se ingresó ningún PIN.",
	KeyConfirmSuffix: " [s/N] ",
	KeySecretWarning: "Esta es la clave secreta de este nodo. No la pegue en chats ni correos.",

	KeyJustNow: "ahora mismo",
	KeySoon:    "en un momento",
	KeyAgo:     "hace %d %s",
	KeyIn:      "en %d %s",

	KeyUnitSecond + ".one":   "segundo",
	KeyUnitSecond + ".other": "segundos",
	KeyUnitMinute + ".one":   "minuto",
	KeyUnitMinute + ".other": "minutos",
	KeyUnitHour + ".one":     "hora",
	KeyUnitHour + ".other":   "horas",
	KeyUnitDay + ".one":      "día",
	KeyUnitDay + ".other":    "días",
	KeyUnitMonth + ".one":    "mes",
	KeyUnitMonth + ".other":  "meses",
	KeyUnitYear + ".one":     "año",
	KeyUnitYear + ".other":   "años",
}
