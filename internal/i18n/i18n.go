// Package i18n trzyma teksty interfejsu po polsku i angielsku.
//
// Bez zewnętrznej biblioteki i bez plików do wczytywania — dwa języki mieszczą
// się w jednej mapie, a dzięki temu binarka nadal nie potrzebuje niczego obok.
package i18n

import (
	"net/http"
	"sort"
	"strings"
)

type Lang string

const (
	PL Lang = "pl"
	EN Lang = "en"

	Ciasteczko = "shyg_lang"
)

var Języki = []Lang{EN, PL}

// Nazwa zwraca nazwę języka w nim samym — do przełącznika.
func (l Lang) Nazwa() string {
	if l == PL {
		return "Polski"
	}
	return "English"
}

func (l Lang) Krótko() string { return strings.ToUpper(string(l)) }

var teksty = map[string]map[Lang]string{
	// nagłówek i panel
	"szukaj":            {EN: "Search services…", PL: "Szukaj usługi…"},
	"filtruj_aria":      {EN: "Filter services", PL: "Filtruj usługi"},
	"usluga":            {EN: "Service", PL: "Usługa"},
	"tryb_edycji":       {EN: "Edit mode (E)", PL: "Tryb edycji (E)"},
	"tryb_edycji_aria":  {EN: "Edit mode", PL: "Tryb edycji"},
	"motyw":             {EN: "Switch theme", PL: "Przełącz motyw"},
	"ustawienia":        {EN: "Settings", PL: "Ustawienia"},
	"jezyk":             {EN: "Language", PL: "Język"},
	"przejdz_do_uslug":  {EN: "Skip to services", PL: "Przejdź do usług"},
	"dodaj_do_grupy":    {EN: "Add a service to this group", PL: "Dodaj usługę do tej grupy"},
	"usun_grupe":        {EN: "Delete group", PL: "Usuń grupę"},
	"zmien_nazwe_grupy": {EN: "Rename group", PL: "Zmień nazwę grupy"},
	"nowa_grupa":        {EN: "+ New group", PL: "+ Nowa grupa"},
	"nic_nie_pasuje":    {EN: "Nothing matches the filter.", PL: "Nic nie pasuje do filtra."},
	"edytuj":            {EN: "Edit", PL: "Edytuj"},
	"usun":              {EN: "Delete", PL: "Usuń"},
	"otworz":            {EN: "Open", PL: "Otwórz"},
	"na_zywo":           {EN: "live", PL: "na żywo"},
	"strumien":          {EN: "Event stream", PL: "Strumień zdarzeń"},
	"wyloguj":           {EN: "log out", PL: "wyloguj"},
	"zapisano":          {EN: "Saved.", PL: "Zapisano."},
	"menu_dodaj_aria":   {EN: "Add…", PL: "Dodaj…"},
	"skroty_podpowiedz": {EN: "/ search · E edit · B background · 1 2 3 views", PL: "/ szukaj · E edycja · B tło · 1 2 3 widoki"},

	// stany usług
	"opis_up":           {EN: "up", PL: "działa"},
	"opis_degraded":     {EN: "responding, but not as expected", PL: "odpowiada, ale nie tak jak powinien"},
	"opis_down":         {EN: "not responding", PL: "nie odpowiada"},
	"opis_unknown":      {EN: "not checked yet", PL: "jeszcze nie sprawdzone"},
	"sprawdzone":        {EN: "checked", PL: "sprawdzone"},
	"licznik_dziala":    {EN: "up", PL: "działa"},
	"licznik_uwagi":     {EN: "need attention", PL: "wymagają uwagi"},
	"licznik_uwagi_1":   {EN: "needs attention", PL: "wymaga uwagi"},
	"licznik_sprawdzam": {EN: "being checked", PL: "w trakcie sprawdzania"},
	"brak_polaczenia":   {EN: "disconnected", PL: "brak połączenia"},
	"dostepnosc_24h":    {EN: "24h uptime", PL: "dostępność 24h"},
	"sprawdzen":         {EN: "checks", PL: "sprawdzeń"},
	"chip_up":           {EN: "up", PL: "działa"},
	"chip_down":         {EN: "down", PL: "nie działa"},
	"chip_degraded":     {EN: "degraded", PL: "słabo"},
	"filtr_problemy":    {EN: "Show only services that need attention", PL: "Pokaż tylko usługi, które wymagają uwagi"},
	"powod_http":        {EN: "HTTP %d", PL: "HTTP %d"},
	"brak_powodu":       {EN: "no reason recorded", PL: "brak zapisanego powodu"},

	// okno usługi
	"nowa_usluga":   {EN: "New service", PL: "Nowa usługa"},
	"edytuj_usluge": {EN: "Edit service", PL: "Edytuj usługę"},
	"nazwa":         {EN: "Name", PL: "Nazwa"},
	"adres":         {EN: "Address", PL: "Adres"},
	"grupa":         {EN: "Group", PL: "Grupa"},
	"ikona":         {EN: "Icon", PL: "Ikona"},
	"szukaj_ikon":   {EN: "Search %d icons…", PL: "Szukaj wśród %d ikon…"},
	"sprawdzanie":   {EN: "Health check", PL: "Sprawdzanie"},
	"co_ile":        {EN: "Interval", PL: "Co ile"},
	"timeout":       {EN: "Timeout", PL: "Timeout"},
	"kody":          {EN: "Accepted status codes", PL: "Akceptowane kody"},
	"insecure":      {EN: "Ignore certificate errors (self-signed, e.g. Proxmox)", PL: "Ignoruj błędy certyfikatu (samopodpisany, np. Proxmox)"},
	"anuluj":        {EN: "Cancel", PL: "Anuluj"},
	"zapisz":        {EN: "Save", PL: "Zapisz"},
	"zamknij":       {EN: "Close", PL: "Zamknij"},

	// ustawienia
	"konta":        {EN: "Accounts", PL: "Konta"},
	"login":        {EN: "login", PL: "login"},
	"haslo_min":    {EN: "password (min. 8)", PL: "hasło (min. 8)"},
	"dodaj":        {EN: "Add", PL: "Dodaj"},
	"zmiana_hasla": {EN: "Change password", PL: "Zmiana hasła"},
	"obecne":       {EN: "current", PL: "obecne"},
	"nowe_min":     {EN: "new (min. 8)", PL: "nowe (min. 8)"},
	"zmien":        {EN: "Change", PL: "Zmień"},
	"plik_konfig":  {EN: "Configuration file", PL: "Plik konfiguracyjny"},
	"plik_konfig_opis": {
		EN: "This is the source of truth. The panel writes changes here without touching your comments or formatting.",
		PL: "To jest źródło prawdy. Panel zapisuje tu zmiany tak, żeby nie ruszyć Twoich komentarzy ani formatowania."},
	"zapisz_plik":      {EN: "Save file", PL: "Zapisz plik"},
	"wczytaj_ponownie": {EN: "Reload", PL: "Wczytaj ponownie"},
	"ty":               {EN: "you", PL: "Ty"},
	"usun_male":        {EN: "delete", PL: "usuń"},
	"haslo_zmienione":  {EN: "Password changed.", PL: "Hasło zmienione."},

	// wygląd i tło
	"wyglad":           {EN: "Appearance", PL: "Wygląd"},
	"tlo":              {EN: "Background", PL: "Tło"},
	"tlo_aria":         {EN: "Background settings (B)", PL: "Ustawienia tła (B)"},
	"tlo_pamietane":    {EN: "Remembered in this browser", PL: "Zapamiętane w tej przeglądarce"},
	"preset_aurora":    {EN: "Aurora", PL: "Zorza"},
	"preset_nebula":    {EN: "Nebula", PL: "Mgławica"},
	"preset_graphite":  {EN: "Graphite", PL: "Grafit"},
	"preset_dawn":      {EN: "Dawn", PL: "Świt"},
	"preset_fog":       {EN: "Fog", PL: "Mgła"},
	"preset_animowany": {EN: "animated", PL: "animowany"},
	"preset_statyczny": {EN: "still", PL: "statyczny"},
	"tapeta":           {EN: "Your wallpaper", PL: "Twoja tapeta"},
	"tapeta_upusc":     {EN: "drop or click", PL: "upuść albo kliknij"},
	"tapeta_podmien":   {EN: "Replace image…", PL: "Podmień obraz…"},
	"tapeta_usun":      {EN: "Remove wallpaper", PL: "Usuń tapetę"},
	"tapeta_tip": {
		EN: "Tip: drop any image anywhere on the page to use it as the wallpaper.",
		PL: "Podpowiedź: upuść dowolny obraz w dowolnym miejscu strony, żeby użyć go jako tapety."},
	"tapeta_wgrana":            {EN: "Wallpaper uploaded.", PL: "Tapeta wgrana."},
	"tapeta_za_duza":           {EN: "The image is larger than %d MB.", PL: "Obraz jest większy niż %d MB."},
	"tapeta_zly_typ":           {EN: "Only JPEG, PNG, WebP, AVIF and GIF are accepted.", PL: "Przyjmujemy tylko JPEG, PNG, WebP, AVIF i GIF."},
	"tapeta_limit":             {EN: "No room for another wallpaper — remove one first.", PL: "Brak miejsca na kolejną tapetę — usuń najpierw jakąś."},
	"tapeta_uzywana":           {EN: "in use", PL: "w użyciu"},
	"tapeta_uzywana_blad":      {EN: "This wallpaper is in use — change the background first.", PL: "Ta tapeta jest w użyciu — zmień najpierw tło."},
	"tapeta_wylaczona":         {EN: "Uploads are off — the data directory is not writable.", PL: "Wgrywanie wyłączone — katalog danych nie jest zapisywalny."},
	"blad_zapisu_tla":          {EN: "Could not upload the image.", PL: "Nie udało się wgrać obrazu."},
	"rozmycie":                 {EN: "Blur", PL: "Rozmycie"},
	"webhook_ze_srodowiska":     {EN: "The webhook is set by an environment variable, so the panel will not change it.", PL: "Webhook jest ustawiony zmienną środowiskową, więc panel go nie zmieni."},
	"webhook_zly_adres":         {EN: "The webhook address has to be an https:// URL.", PL: "Adres webhooka musi być adresem https://."},
	"webhook_zly_prog":          {EN: "The failure threshold has to be between 1 and 10.", PL: "Próg awarii musi mieścić się między 1 a 10."},
	"webhook_zrodlo_brak":       {EN: "No webhook set — notifications are off.", PL: "Webhook nieustawiony — powiadomienia wyłączone."},
	"docker_wylaczony":          {EN: "Suggesting services from containers is turned off in the config file.", PL: "Podpowiadanie usług z kontenerów jest wyłączone w pliku konfiguracyjnym."},
	"zakladka_wyglad":          {EN: "Appearance", PL: "Wygląd"},
	"zakladka_powiadomienia":   {EN: "Notifications", PL: "Powiadomienia"},
	"zakladka_konta":           {EN: "Accounts", PL: "Konta"},
	"zakladka_historia":        {EN: "History", PL: "Historia"},
	"zakladka_plik":            {EN: "File", PL: "Plik"},
	"zakladka_instancja":       {EN: "Instance", PL: "Instancja"},
	"pelny_ekran":              {EN: "Full screen (Esc to leave)", PL: "Pełny ekran (Esc wychodzi)"},
	"wyglad_instancji_opis":    {EN: "The default for everyone. Your own picker in the top bar only changes this browser.", PL: "Domyślne dla wszystkich. Picker w pasku zmienia wyłącznie tę przeglądarkę."},
	"przyciemnienie":           {EN: "Dimming", PL: "Przyciemnienie"},
	"powiadomienia_opis":       {EN: "One sentence on Discord when a service goes down and when it comes back. Nothing else.", PL: "Jedno zdanie na Discordzie, gdy usługa padnie i gdy wróci. Nic poza tym."},
	"webhook_adres":            {EN: "Webhook address", PL: "Adres webhooka"},
	"webhook_prog":             {EN: "Failures before notifying", PL: "Awarii przed powiadomieniem"},
	"webhook_zapisany":         {EN: "Saved.", PL: "Zapisane."},
	"instancja_opis":           {EN: "Set at startup with a flag or an environment variable. Shown here so you know where each value came from.", PL: "Ustawiane przy starcie flagą albo zmienną środowiskową. Pokazane tutaj, żeby było wiadomo, skąd wzięła się każda wartość."},
	"instancja_opcja":          {EN: "Option", PL: "Opcja"},
	"instancja_wartosc":        {EN: "Value", PL: "Wartość"},
	"instancja_zrodlo":         {EN: "Source", PL: "Źródło"},
	"zrodlo_flaga":             {EN: "flag", PL: "flaga"},
	"zrodlo_srodowisko":        {EN: "environment", PL: "środowisko"},
	"zrodlo_domyslna":          {EN: "default", PL: "domyślna"},
	"wyglad_przywroc_instancji": {EN: "Back to the instance default", PL: "Przywróć domyślne instancji"},
	"wyglad_dla_wszystkich":    {EN: "Set for everyone →", PL: "Ustaw dla wszystkich →"},
	"ton":                      {EN: "Text", PL: "Tekst"},
	"ton_jasny":                {EN: "Light text", PL: "Jasny tekst"},
	"ton_ciemny":               {EN: "Dark text", PL: "Ciemny tekst"},
	"wyglad_zapisz_domyslny":   {EN: "Save as the instance default", PL: "Zapisz jako domyślny instancji"},
	"wyglad_zapisany":          {EN: "Appearance saved to the file.", PL: "Wygląd zapisany do pliku."},
	"blad_wygladu":             {EN: "Invalid appearance value.", PL: "Nieprawidłowa wartość wyglądu."},
	"widok":                    {EN: "View", PL: "Widok"},
	"widok_grid":               {EN: "Grid · 1", PL: "Siatka · 1"},
	"widok_bento":              {EN: "Bento · 2", PL: "Bento · 2"},
	"widok_lista":              {EN: "List · 3", PL: "Lista · 3"},
	"rozmiar":                  {EN: "Card size", PL: "Rozmiar kafla"},
	"rozmiar_sm":               {EN: "Small", PL: "Mały"},
	"rozmiar_md":               {EN: "Medium", PL: "Średni"},
	"rozmiar_lg":               {EN: "Large", PL: "Duży"},

	// opisy zmian — trafiają do historii i do wiadomości commita
	"opis_dodano_usluge":    {EN: `added service %q to group %q`, PL: `dodano usługę %q do grupy %q`},
	"opis_zmieniono_usluge": {EN: `changed service %q`, PL: `zmieniono usługę %q`},
	"opis_przeniesiono":     {EN: `moved service %q from %q to %q`, PL: `przeniesiono usługę %q z %q do %q`},
	"opis_usunieto_usluge":  {EN: `deleted service %q from group %q`, PL: `usunięto usługę %q z grupy %q`},
	"opis_dodano_grupe":     {EN: `added group %q`, PL: `dodano grupę %q`},
	"opis_zmieniono_grupe":  {EN: `renamed group %q to %q`, PL: `zmieniono nazwę grupy %q na %q`},
	"opis_usunieto_grupe":   {EN: `deleted group %q`, PL: `usunięto grupę %q`},
	"opis_dodano_wiele":     {EN: `added %d services to group %q`, PL: `dodano %d usług do grupy %q`},
	"opis_zmieniono_wyglad": {EN: `changed appearance (%s)`, PL: `zmieniono wygląd (%s)`},
	"opis_reczna_edycja":    {EN: "manual file edit", PL: "ręczna edycja pliku"},
	"opis_przywrocono":      {EN: `restored version #%d (%s)`, PL: `przywrócono wersję #%d (%s)`},

	// szczegóły usługi
	"szczegoly":        {EN: "Details", PL: "Szczegóły"},
	"sprawdz_teraz":    {EN: "Check now", PL: "Sprawdź teraz"},
	"zmiany_stanu":     {EN: "State changes", PL: "Zmiany stanu"},
	"brak_zmian_stanu": {EN: "No state changes recorded yet.", PL: "Jeszcze nie odnotowano zmian stanu."},
	"wykres_opis":      {EN: "Response time over recent checks", PL: "Czas odpowiedzi w ostatnich sprawdzeniach"},
	"doba":             {EN: "24 h", PL: "24 h"},
	"tydzien":          {EN: "7 days", PL: "7 dni"},
	"mediana":          {EN: "median", PL: "mediana"},
	"najwolniej":       {EN: "slowest", PL: "najwolniej"},
	"brak_danych":      {EN: "no data yet", PL: "brak danych"},
	"sprawdzam":        {EN: "checking…", PL: "sprawdzam…"},

	// podpowiadanie z Dockera
	"znajdz_uslugi":      {EN: "Find services", PL: "Znajdź usługi"},
	"docker_niedostepny": {EN: "Docker socket not available", PL: "Gniazdo Dockera niedostępne"},
	"menu_docker_nowe":   {EN: "%d new", PL: "%d nowych"},
	"docker_tytul":       {EN: "Containers running on this host", PL: "Kontenery działające na tym hoście"},
	"docker_opis":        {EN: "Pick what belongs on the dashboard. Nothing is written until you confirm the diff.", PL: "Zaznacz, co ma trafić na panel. Nic nie zostanie zapisane, dopóki nie potwierdzisz diffa."},
	"docker_brak":        {EN: "No containers found, or the Docker socket is not mounted.", PL: "Nie znaleziono kontenerów albo gniazdo Dockera nie jest zamontowane."},
	"docker_juz_jest":    {EN: "already on the dashboard", PL: "już jest na panelu"},
	"why_label":          {EN: "address from a shyg.url label", PL: "adres z etykiety shyg.url"},
	"why_port":           {EN: "port published on the host", PL: "port wystawiony na hoście"},
	"why_noport":         {EN: "no published port — set the address yourself", PL: "brak wystawionego portu — podaj adres ręcznie"},
	"dodaj_zaznaczone":   {EN: "Add selected", PL: "Dodaj zaznaczone"},
	"do_grupy":           {EN: "to group", PL: "do grupy"},

	// powiadomienia
	"powiadomienia":    {EN: "Notifications", PL: "Powiadomienia"},
	"powiad_wlaczone":  {EN: "Webhook configured — you get a message when a service goes down and when it comes back.", PL: "Webhook skonfigurowany — dostajesz wiadomość, gdy usługa padnie i gdy wróci."},
	"powiad_wylaczone": {EN: "No webhook. Paste one above (a Discord webhook works) to get notified.", PL: "Brak webhooka. Wklej go wyżej (webhook Discorda działa), żeby dostawać powiadomienia."},
	"wyslij_test":      {EN: "Send a test message", PL: "Wyślij wiadomość próbną"},
	"test_wyslany":     {EN: "Test message sent.", PL: "Wiadomość próbna wysłana."},

	// podgląd zmian i historia
	"podglad_zmian":      {EN: "Review the change", PL: "Podgląd zmiany"},
	"podglad_opis":       {EN: "This is exactly what will change in your file. Nothing else is touched.", PL: "Dokładnie to zmieni się w Twoim pliku. Nic poza tym nie zostanie ruszone."},
	"zapisz_zmiane":      {EN: "Save to file", PL: "Zapisz do pliku"},
	"bez_zmian":          {EN: "Nothing would change.", PL: "Nic by się nie zmieniło."},
	"linii_dodanych":     {EN: "added", PL: "dodanych"},
	"linii_usunietych":   {EN: "removed", PL: "usuniętych"},
	"historia_zmian":     {EN: "Change history", PL: "Historia zmian"},
	"historia_opis":      {EN: "Every save is kept. You can go back to any of them.", PL: "Każdy zapis jest zachowany. Możesz wrócić do dowolnego."},
	"przywroc":           {EN: "restore", PL: "przywróć"},
	"pokaz":              {EN: "show", PL: "pokaż"},
	"brak_historii":      {EN: "No changes yet.", PL: "Jeszcze nic nie zmieniano."},
	"git_aktywny":        {EN: "config is in a git repository — every save is committed", PL: "config leży w repozytorium git — każdy zapis jest commitowany"},
	"potwierdz_przywroc": {EN: "Restore the file to version #%s?", PL: "Przywrócić plik do wersji #%s?"},

	// potwierdzenia i mini-dialogi
	"nazwa_grupy_tytul":    {EN: "Group name", PL: "Nazwa grupy"},
	"pytanie_nazwa_grupy":  {EN: "New group name:", PL: "Nazwa nowej grupy:"},
	"potwierdzenie_tytul":  {EN: "Are you sure?", PL: "Na pewno?"},
	"potwierdz_tak":        {EN: "Yes, delete", PL: "Tak, usuń"},
	"potwierdz_usun_konto": {EN: "Delete account %s?", PL: "Usunąć konto %s?"},

	// logowanie
	"logowanie":        {EN: "Sign in", PL: "Logowanie"},
	"pierwsze_urucho":  {EN: "First run", PL: "Pierwsze uruchomienie"},
	"zaloguj":          {EN: "Sign in", PL: "Zaloguj"},
	"zaloz_konto":      {EN: "Create account", PL: "Załóż konto"},
	"haslo":            {EN: "Password", PL: "Hasło"},
	"opis_logowania":   {EN: "Sign in to see the dashboard.", PL: "Zaloguj się, żeby zobaczyć panel."},
	"opis_setup":       {EN: "First run. Create the account you will sign in with.", PL: "Pierwsze uruchomienie. Załóż konto, którym będziesz się logować."},
	"min_8":            {EN: "Minimum 8 characters.", PL: "Minimum 8 znaków."},
	"zle_dane":         {EN: "Wrong login or password.", PL: "Zły login albo hasło."},
	"za_duzo_prob":     {EN: "Too many attempts. Try again in five minutes.", PL: "Za dużo prób. Spróbuj za pięć minut."},
	"blad_konta":       {EN: "Could not create the account.", PL: "Nie udało się utworzyć konta."},
	"haslo_za_krotkie": {EN: "Password must be at least 8 characters.", PL: "Hasło musi mieć co najmniej 8 znaków."},
	"login_za_krotki":  {EN: "Login must be at least 2 characters.", PL: "Login musi mieć co najmniej 2 znaki."},

	// błędy API pokazywane userowi
	"konflikt_pliku": {
		EN: "the file changed on disk since it was loaded — refresh the page and repeat the change",
		PL: "plik zmienił się na dysku od czasu wczytania — odśwież stronę i powtórz zmianę"},
}

// T zwraca tekst w danym języku; przy braku tłumaczenia wraca do angielskiego.
func T(l Lang, klucz string) string {
	m, ok := teksty[klucz]
	if !ok {
		return klucz
	}
	if v, ok := m[l]; ok && v != "" {
		return v
	}
	return m[EN]
}

// Mapa zwraca komplet tekstów dla języka — wygodne dla szablonów i dla JS-a.
func Mapa(l Lang) map[string]string {
	out := make(map[string]string, len(teksty))
	for k := range teksty {
		out[k] = T(l, k)
	}
	return out
}

// Klucze zwraca posortowaną listę kluczy — używane w testach spójności.
func Klucze() []string {
	out := make([]string, 0, len(teksty))
	for k := range teksty {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Wybierz ustala język: ciasteczko użytkownika, potem Accept-Language,
// a w ostateczności angielski.
func Wybierz(r *http.Request) Lang {
	if c, err := r.Cookie(Ciasteczko); err == nil {
		if l := Rozpoznaj(c.Value); l != "" {
			return l
		}
	}
	for _, część := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		kod := strings.TrimSpace(strings.SplitN(część, ";", 2)[0])
		if l := Rozpoznaj(kod); l != "" {
			return l
		}
	}
	return EN
}

// Rozpoznaj mapuje kod języka na obsługiwany; pusty string = nieobsługiwany.
func Rozpoznaj(kod string) Lang {
	kod = strings.ToLower(strings.TrimSpace(kod))
	switch {
	case kod == "pl" || strings.HasPrefix(kod, "pl-"):
		return PL
	case kod == "en" || strings.HasPrefix(kod, "en-"):
		return EN
	}
	return ""
}
