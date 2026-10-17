// Package i18n holds the interface texts in Polish and English.
//
// No external library and no files to load — two languages fit
// in a single map, and thanks to that the binary still needs nothing next to it.
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

	CookieName = "shyg_lang"
)

var Languages = []Lang{EN, PL}

// Name returns the language's name in itself — for the switcher.
func (l Lang) Name() string {
	if l == PL {
		return "Polski"
	}
	return "English"
}

func (l Lang) Short() string { return strings.ToUpper(string(l)) }

var texts = map[string]map[Lang]string{
	// header and panel
	"search_placeholder": {EN: "Search services…", PL: "Szukaj usługi…"},
	"filter_aria":        {EN: "Filter services", PL: "Filtruj usługi"},
	"service":            {EN: "Service", PL: "Usługa"},
	"edit_mode":          {EN: "Edit mode (E)", PL: "Tryb edycji (E)"},
	"edit_mode_aria":     {EN: "Edit mode", PL: "Tryb edycji"},
	"settings":           {EN: "Settings", PL: "Ustawienia"},
	"language":           {EN: "Language", PL: "Język"},
	"skip_to_services":   {EN: "Skip to services", PL: "Przejdź do usług"},
	"add_to_group":       {EN: "Add a service to this group", PL: "Dodaj usługę do tej grupy"},
	"delete_group":       {EN: "Delete group", PL: "Usuń grupę"},
	"rename_group":       {EN: "Rename group", PL: "Zmień nazwę grupy"},
	"new_group":          {EN: "+ New group", PL: "+ Nowa grupa"},
	"edit_title":         {EN: "Edit title", PL: "Edytuj tytuł"},
	"title_field_aria":   {EN: "Dashboard title", PL: "Tytuł panelu"},
	"handle_service":     {EN: "Move service (drag, or Alt+↑/↓)", PL: "Przenieś usługę (przeciągnij albo Alt+↑/↓)"},
	"handle_group":       {EN: "Move group (drag, or Alt+↑/↓)", PL: "Przenieś grupę (przeciągnij albo Alt+↑/↓)"},
	"position_service":   {EN: "%s: position %d of %d in group %s", PL: "%s: pozycja %d z %d w grupie %s"},
	"position_group":     {EN: "Group %s: position %d of %d", PL: "Grupa %s: pozycja %d z %d"},
	"move_filter_active": {EN: "Clear the filter to rearrange.", PL: "Wyczyść filtr, żeby zmienić kolejność."},
	"order_saved":        {EN: "Order saved.", PL: "Zapisano kolejność."},
	"no_match":           {EN: "Nothing matches the filter.", PL: "Nic nie pasuje do filtra."},
	"edit":               {EN: "Edit", PL: "Edytuj"},
	"delete":             {EN: "Delete", PL: "Usuń"},
	"open":               {EN: "Open", PL: "Otwórz"},
	"live":               {EN: "live", PL: "na żywo"},
	"event_stream":       {EN: "Event stream", PL: "Strumień zdarzeń"},
	"log_out":            {EN: "log out", PL: "wyloguj"},
	"saved":              {EN: "Saved.", PL: "Zapisano."},
	"menu_add_aria":      {EN: "Add…", PL: "Dodaj…"},
	"shortcuts_hint":     {EN: "/ search · E edit · B background · 1 2 3 views", PL: "/ szukaj · E edycja · B tło · 1 2 3 widoki"},

	// service states
	"state_desc_up":       {EN: "up", PL: "działa"},
	"state_desc_degraded": {EN: "responding, but not as expected", PL: "odpowiada, ale nie tak jak powinien"},
	"state_desc_down":     {EN: "not responding", PL: "nie odpowiada"},
	"state_desc_unknown":  {EN: "not checked yet", PL: "jeszcze nie sprawdzone"},
	"checked":             {EN: "checked", PL: "sprawdzone"},
	"count_up":            {EN: "up", PL: "działa"},
	"count_attention":     {EN: "need attention", PL: "wymagają uwagi"},
	"count_attention_one": {EN: "needs attention", PL: "wymaga uwagi"},
	"count_checking":      {EN: "being checked", PL: "w trakcie sprawdzania"},
	"disconnected":        {EN: "disconnected", PL: "brak połączenia"},
	"uptime_24h":          {EN: "24h uptime", PL: "dostępność 24h"},
	"checks":              {EN: "checks", PL: "sprawdzeń"},
	"chip_up":             {EN: "up", PL: "działa"},
	"chip_down":           {EN: "down", PL: "nie działa"},
	"chip_degraded":       {EN: "degraded", PL: "słabo"},
	"filter_problems":     {EN: "Show only services that need attention", PL: "Pokaż tylko usługi, które wymagają uwagi"},
	"reason_http":         {EN: "HTTP %d", PL: "HTTP %d"},
	"reason_none":         {EN: "no reason recorded", PL: "brak zapisanego powodu"},

	// service window
	"new_service":  {EN: "New service", PL: "Nowa usługa"},
	"edit_service": {EN: "Edit service", PL: "Edytuj usługę"},
	"name":         {EN: "Name", PL: "Nazwa"},
	"address":      {EN: "Address", PL: "Adres"},
	"group":        {EN: "Group", PL: "Grupa"},
	"icon":         {EN: "Icon", PL: "Ikona"},
	"search_icons": {EN: "Search %d icons…", PL: "Szukaj wśród %d ikon…"},
	"health_check": {EN: "Health check", PL: "Sprawdzanie"},
	"interval":     {EN: "Interval", PL: "Co ile"},
	"timeout":      {EN: "Timeout", PL: "Timeout"},
	"status_codes": {EN: "Accepted status codes", PL: "Akceptowane kody"},
	"insecure":     {EN: "Ignore certificate errors (self-signed, e.g. Proxmox)", PL: "Ignoruj błędy certyfikatu (samopodpisany, np. Proxmox)"},
	"cancel":       {EN: "Cancel", PL: "Anuluj"},
	"save":         {EN: "Save", PL: "Zapisz"},
	"close":        {EN: "Close", PL: "Zamknij"},

	// settings
	"accounts":        {EN: "Accounts", PL: "Konta"},
	"login":           {EN: "login", PL: "login"},
	"password_min":    {EN: "password (min. 8)", PL: "hasło (min. 8)"},
	"add":             {EN: "Add", PL: "Dodaj"},
	"change_password": {EN: "Change password", PL: "Zmiana hasła"},
	"current":         {EN: "current", PL: "obecne"},
	"new_min":         {EN: "new (min. 8)", PL: "nowe (min. 8)"},
	"change":          {EN: "Change", PL: "Zmień"},
	"config_file_desc": {
		EN: "This is the source of truth. The panel writes changes here without touching your comments or formatting.",
		PL: "To jest źródło prawdy. Panel zapisuje tu zmiany tak, żeby nie ruszyć Twoich komentarzy ani formatowania."},
	"save_file":        {EN: "Save file", PL: "Zapisz plik"},
	"reload":           {EN: "Reload", PL: "Wczytaj ponownie"},
	"you":              {EN: "you", PL: "Ty"},
	"delete_lower":     {EN: "delete", PL: "usuń"},
	"password_changed": {EN: "Password changed. Other sessions were signed out. API tokens stay valid — revoke them under API tokens if you no longer trust one.", PL: "Hasło zmienione. Pozostałe sesje wylogowano. Tokeny API dalej działają — odwołaj je w sekcji Tokeny API, jeśli któremuś już nie ufasz."},

	// appearance and background
	"appearance":            {EN: "Appearance", PL: "Wygląd"},
	"background":            {EN: "Background", PL: "Tło"},
	"background_aria":       {EN: "Background settings (B)", PL: "Ustawienia tła (B)"},
	"background_remembered": {EN: "Remembered in this browser", PL: "Zapamiętane w tej przeglądarce"},
	"preset_aurora":         {EN: "Aurora", PL: "Zorza"},
	"preset_nebula":         {EN: "Nebula", PL: "Mgławica"},
	"preset_graphite":       {EN: "Graphite", PL: "Grafit"},
	"preset_dawn":           {EN: "Dawn", PL: "Świt"},
	"preset_fog":            {EN: "Fog", PL: "Mgła"},
	"preset_animated":       {EN: "animated", PL: "animowany"},
	"preset_still":          {EN: "still", PL: "statyczny"},
	"wallpaper":             {EN: "Your wallpaper", PL: "Twoja tapeta"},
	"wallpaper_drop":        {EN: "drop or click", PL: "upuść albo kliknij"},
	"wallpaper_replace":     {EN: "Replace image…", PL: "Podmień obraz…"},
	"wallpaper_remove":      {EN: "Remove wallpaper", PL: "Usuń tapetę"},
	"wallpaper_tip": {
		EN: "Tip: drop any image anywhere on the page to use it as the wallpaper.",
		PL: "Podpowiedź: upuść dowolny obraz w dowolnym miejscu strony, żeby użyć go jako tapety."},
	"wallpaper_uploaded":        {EN: "Wallpaper uploaded.", PL: "Tapeta wgrana."},
	"wallpaper_too_large":       {EN: "The image is larger than %d MB.", PL: "Obraz jest większy niż %d MB."},
	"wallpaper_bad_type":        {EN: "Only JPEG, PNG, WebP, AVIF and GIF are accepted.", PL: "Przyjmujemy tylko JPEG, PNG, WebP, AVIF i GIF."},
	"wallpaper_limit":           {EN: "No room for another wallpaper — remove one first.", PL: "Brak miejsca na kolejną tapetę — usuń najpierw jakąś."},
	"wallpaper_in_use":          {EN: "in use", PL: "w użyciu"},
	"error_wallpaper_in_use":    {EN: "This wallpaper is in use — change the background first.", PL: "Ta tapeta jest w użyciu — zmień najpierw tło."},
	"wallpaper_uploads_off":     {EN: "Uploads are off — the data directory is not writable.", PL: "Wgrywanie wyłączone — katalog danych nie jest zapisywalny."},
	"error_wallpaper_upload":    {EN: "Could not upload the image.", PL: "Nie udało się wgrać obrazu."},
	"blur":                      {EN: "Blur", PL: "Rozmycie"},
	"webhook_from_env":          {EN: "The webhook is set by an environment variable, so the panel will not change it.", PL: "Webhook jest ustawiony zmienną środowiskową, więc panel go nie zmieni."},
	"webhook_bad_address":       {EN: "The webhook address has to be an https:// URL.", PL: "Adres webhooka musi być adresem https://."},
	"webhook_bad_threshold":     {EN: "The failure threshold has to be between 1 and 10.", PL: "Próg awarii musi mieścić się między 1 a 10."},
	"webhook_not_set":           {EN: "No webhook set — notifications are off.", PL: "Webhook nieustawiony — powiadomienia wyłączone."},
	"docker_disabled":           {EN: "Suggesting services from containers is turned off in the config file.", PL: "Podpowiadanie usług z kontenerów jest wyłączone w pliku konfiguracyjnym."},
	"tab_appearance":            {EN: "Appearance", PL: "Wygląd"},
	"tab_notifications":         {EN: "Notifications", PL: "Powiadomienia"},
	"tab_accounts":              {EN: "Accounts", PL: "Konta"},
	"tab_history":               {EN: "History", PL: "Historia"},
	"tab_file":                  {EN: "File", PL: "Plik"},
	"tab_instance":              {EN: "Instance", PL: "Instancja"},
	"fullscreen":                {EN: "Full screen (Esc to leave)", PL: "Pełny ekran (Esc wychodzi)"},
	"appearance_instance_desc":  {EN: "The default for everyone. Your own picker in the top bar only changes this browser.", PL: "Domyślne dla wszystkich. Picker w pasku zmienia wyłącznie tę przeglądarkę."},
	"dim":                       {EN: "Dimming", PL: "Przyciemnienie"},
	"notifications_desc":        {EN: "One short message when a service goes down and when it comes back. Nothing else.", PL: "Jedna krótka wiadomość, gdy usługa padnie i gdy wróci. Nic poza tym."},
	"webhook_address":           {EN: "Webhook address", PL: "Adres webhooka"},
	"webhook_threshold":         {EN: "Failures before notifying", PL: "Awarii przed powiadomieniem"},
	"webhook_saved":             {EN: "Saved.", PL: "Zapisane."},
	"instance_desc":             {EN: "Set at startup with a flag or an environment variable. Shown here so you know where each value came from.", PL: "Ustawiane przy starcie flagą albo zmienną środowiskową. Pokazane tutaj, żeby było wiadomo, skąd wzięła się każda wartość."},
	"instance_option":           {EN: "Option", PL: "Opcja"},
	"instance_value":            {EN: "Value", PL: "Wartość"},
	"instance_source":           {EN: "Source", PL: "Źródło"},
	"source_flag":               {EN: "flag", PL: "flaga"},
	"source_env":                {EN: "environment", PL: "środowisko"},
	"source_default":            {EN: "default", PL: "domyślna"},
	"appearance_reset_instance": {EN: "Back to the instance default", PL: "Przywróć domyślne instancji"},
	"appearance_set_for_all":    {EN: "Set for everyone →", PL: "Ustaw dla wszystkich →"},
	"tone":                      {EN: "Text", PL: "Tekst"},
	"tone_light":                {EN: "Light text", PL: "Jasny tekst"},
	"tone_dark":                 {EN: "Dark text", PL: "Ciemny tekst"},
	"appearance_save_default":   {EN: "Save as the instance default", PL: "Zapisz jako domyślny instancji"},
	"appearance_saved":          {EN: "Appearance saved to the file.", PL: "Wygląd zapisany do pliku."},
	"error_appearance":          {EN: "Invalid appearance value.", PL: "Nieprawidłowa wartość wyglądu."},
	"view":                      {EN: "View", PL: "Widok"},
	"view_grid":                 {EN: "Grid · 1", PL: "Siatka · 1"},
	"view_bento":                {EN: "Bento · 2", PL: "Bento · 2"},
	"view_list":                 {EN: "List · 3", PL: "Lista · 3"},
	"size":                      {EN: "Card size", PL: "Rozmiar kafla"},
	"size_sm":                   {EN: "Small", PL: "Mały"},
	"size_md":                   {EN: "Medium", PL: "Średni"},
	"size_lg":                   {EN: "Large", PL: "Duży"},

	// change descriptions — they go into the history and into the commit message
	"desc_service_added":      {EN: `added service %q to group %q`, PL: `dodano usługę %q do grupy %q`},
	"desc_service_changed":    {EN: `changed service %q`, PL: `zmieniono usługę %q`},
	"desc_service_moved":      {EN: `moved service %q from %q to %q`, PL: `przeniesiono usługę %q z %q do %q`},
	"desc_service_deleted":    {EN: `deleted service %q from group %q`, PL: `usunięto usługę %q z grupy %q`},
	"desc_group_added":        {EN: `added group %q`, PL: `dodano grupę %q`},
	"desc_title_changed":      {EN: `changed the dashboard title to %q`, PL: `zmieniono tytuł panelu na %q`},
	"desc_service_reordered":  {EN: `moved service %q in group %q to position %d`, PL: `przesunięto usługę %q w grupie %q na pozycję %d`},
	"desc_group_reordered":    {EN: `moved group %q to position %d`, PL: `przesunięto grupę %q na pozycję %d`},
	"desc_group_renamed":      {EN: `renamed group %q to %q`, PL: `zmieniono nazwę grupy %q na %q`},
	"desc_group_deleted":      {EN: `deleted group %q`, PL: `usunięto grupę %q`},
	"desc_services_added":     {EN: `added %d services to group %q`, PL: `dodano %d usług do grupy %q`},
	"desc_appearance_changed": {EN: `changed appearance (%s)`, PL: `zmieniono wygląd (%s)`},
	"desc_notify_threshold":   {EN: "changed the notification threshold", PL: "zmieniono próg powiadomień"},
	"desc_manual_edit":        {EN: "manual file edit", PL: "ręczna edycja pliku"},
	"desc_version_restored":   {EN: `restored version #%d (%s)`, PL: `przywrócono wersję #%d (%s)`},

	// service details
	"details":            {EN: "Details", PL: "Szczegóły"},
	"check_now":          {EN: "Check now", PL: "Sprawdź teraz"},
	"state_changes":      {EN: "State changes", PL: "Zmiany stanu"},
	"state_changes_none": {EN: "No state changes recorded yet.", PL: "Jeszcze nie odnotowano zmian stanu."},
	"chart_desc":         {EN: "Response time over recent checks", PL: "Czas odpowiedzi w ostatnich sprawdzeniach"},
	"period_day":         {EN: "24 h", PL: "24 h"},
	"period_week":        {EN: "7 days", PL: "7 dni"},
	"median":             {EN: "median", PL: "mediana"},
	"slowest":            {EN: "slowest", PL: "najwolniej"},
	"no_data":            {EN: "no data yet", PL: "brak danych"},
	"checking":           {EN: "checking…", PL: "sprawdzam…"},

	// suggestions from Docker
	"find_services":        {EN: "Find services", PL: "Znajdź usługi"},
	"docker_unavailable":   {EN: "Docker socket not available", PL: "Gniazdo Dockera niedostępne"},
	"menu_docker_new":      {EN: "%d new", PL: "%d nowych"},
	"docker_title":         {EN: "Containers running on this host", PL: "Kontenery działające na tym hoście"},
	"docker_desc":          {EN: "Pick what belongs on the dashboard. Nothing is written until you confirm the diff.", PL: "Zaznacz, co ma trafić na panel. Nic nie zostanie zapisane, dopóki nie potwierdzisz diffa."},
	"docker_none":          {EN: "No containers found, or the Docker socket is not mounted.", PL: "Nie znaleziono kontenerów albo gniazdo Dockera nie jest zamontowane."},
	"docker_already_added": {EN: "already on the dashboard", PL: "już jest na panelu"},
	"why_label":            {EN: "address from a shyg.url label", PL: "adres z etykiety shyg.url"},
	"why_port":             {EN: "port published on the host", PL: "port wystawiony na hoście"},
	"why_noport":           {EN: "no published port — set the address yourself", PL: "brak wystawionego portu — podaj adres ręcznie"},
	"add_selected":         {EN: "Add selected", PL: "Dodaj zaznaczone"},
	"to_group":             {EN: "to group", PL: "do grupy"},

	// notifications
	"notify_enabled":  {EN: "Webhook configured — you get a message when a service goes down and when it comes back.", PL: "Webhook skonfigurowany — dostajesz wiadomość, gdy usługa padnie i gdy wróci."},
	"notify_disabled": {EN: "No webhook. Paste one above (a Discord webhook works) to get notified.", PL: "Brak webhooka. Wklej go wyżej (webhook Discorda działa), żeby dostawać powiadomienia."},
	"send_test":       {EN: "Send a test message", PL: "Wyślij wiadomość próbną"},
	"test_sent":       {EN: "Test message sent.", PL: "Wiadomość próbna wysłana."},

	// change preview and history
	"diff_title":     {EN: "Review the change", PL: "Podgląd zmiany"},
	"diff_desc":      {EN: "This is exactly what will change in your file. Nothing else is touched.", PL: "Dokładnie to zmieni się w Twoim pliku. Nic poza tym nie zostanie ruszone."},
	"diff_save":      {EN: "Save to file", PL: "Zapisz do pliku"},
	"diff_no_change": {EN: "Nothing would change.", PL: "Nic by się nie zmieniło."},
	"diff_added":     {EN: "added", PL: "dodanych"},
	"diff_removed":   {EN: "removed", PL: "usuniętych"},
	"history_desc":   {EN: "Every save is kept. You can go back to any of them.", PL: "Każdy zapis jest zachowany. Możesz wrócić do dowolnego."},
	"restore":        {EN: "restore", PL: "przywróć"},
	"show":           {EN: "show", PL: "pokaż"},
	"history_none":   {EN: "No changes yet.", PL: "Jeszcze nic nie zmieniano."},
	"git_active":     {EN: "config is in a git repository — every save is committed", PL: "config leży w repozytorium git — każdy zapis jest commitowany"},

	// confirmations and mini-dialogs
	"group_name_title":       {EN: "Group name", PL: "Nazwa grupy"},
	"confirm_title":          {EN: "Are you sure?", PL: "Na pewno?"},
	"confirm_yes_delete":     {EN: "Yes, delete", PL: "Tak, usuń"},
	"confirm_delete_account": {EN: "Delete account %s?", PL: "Usunąć konto %s?"},

	// login
	"sign_in":              {EN: "Sign in", PL: "Logowanie"},
	"first_run":            {EN: "First run", PL: "Pierwsze uruchomienie"},
	"sign_in_button":       {EN: "Sign in", PL: "Zaloguj"},
	"create_account":       {EN: "Create account", PL: "Załóż konto"},
	"password":             {EN: "Password", PL: "Hasło"},
	"sign_in_desc":         {EN: "Sign in to see the dashboard.", PL: "Zaloguj się, żeby zobaczyć panel."},
	"setup_desc":           {EN: "First run. Create the account you will sign in with.", PL: "Pierwsze uruchomienie. Załóż konto, którym będziesz się logować."},
	"min_8":                {EN: "Minimum 8 characters.", PL: "Minimum 8 znaków."},
	"bad_credentials":      {EN: "Wrong login or password.", PL: "Zły login albo hasło."},
	"too_many_attempts":    {EN: "Too many attempts. Try again in five minutes.", PL: "Za dużo prób. Spróbuj za pięć minut."},
	"error_account":        {EN: "Could not create the account.", PL: "Nie udało się utworzyć konta."},
	"error_password_short": {EN: "Password must be at least 8 characters.", PL: "Hasło musi mieć co najmniej 8 znaków."},
	"error_login_short":    {EN: "Login must be at least 2 characters.", PL: "Login musi mieć co najmniej 2 znaki."},

	// API errors shown to the user
	"error_title_empty": {EN: "The title cannot be empty.", PL: "Tytuł nie może być pusty."},
	"error_title_long":  {EN: "The title is too long (max 80 characters).", PL: "Tytuł jest za długi (maks. 80 znaków)."},
	// webhook formats (SHYG-50)
	"webhook_format":           {EN: "Message format", PL: "Format wiadomości"},
	"format_generic":           {EN: "Generic JSON", PL: "Ogólny JSON"},
	"webhook_bad_format":       {EN: "Unknown message format.", PL: "Nieznany format wiadomości."},
	"webhook_format_from_env":  {EN: "The message format is set by SHYG_WEBHOOK_FORMAT (or --webhook-format), so it cannot be changed here.", PL: "Format wiadomości jest ustawiony przez SHYG_WEBHOOK_FORMAT (albo --webhook-format), więc nie da się go zmienić tutaj."},
	"webhook_address_telegram": {EN: "For Telegram the address has to look like https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<ID>.", PL: "Dla Telegrama adres musi wyglądać tak: https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<ID>."},
	"webhook_address_gotify":   {EN: "For Gotify the address has to look like https://<host>/message?token=<APP_TOKEN>.", PL: "Dla Gotify adres musi wyglądać tak: https://<host>/message?token=<APP_TOKEN>."},
	"format_hint_discord":      {EN: "Discord webhook: https://discord.com/api/webhooks/<id>/<token>", PL: "Webhook Discorda: https://discord.com/api/webhooks/<id>/<token>"},
	"format_hint_slack":        {EN: "Slack incoming webhook (Mattermost and Rocket.Chat accept the same): https://hooks.slack.com/services/…", PL: "Przychodzący webhook Slacka (Mattermost i Rocket.Chat przyjmują ten sam): https://hooks.slack.com/services/…"},
	"format_hint_ntfy":         {EN: "ntfy topic: https://ntfy.sh/<topic>, or your own server. Credentials can go in the address: https://user:password@ntfy.example/<topic>", PL: "Temat ntfy: https://ntfy.sh/<temat> albo Twój serwer. Dane logowania mogą być w adresie: https://user:hasło@ntfy.example/<temat>"},
	"format_hint_gotify":       {EN: "Gotify, with the application token in the address: https://<host>/message?token=<APP_TOKEN>", PL: "Gotify, z tokenem aplikacji w adresie: https://<host>/message?token=<APP_TOKEN>"},
	"format_hint_telegram":     {EN: "Telegram bot: https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<CHAT_ID>", PL: "Bot Telegrama: https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<CHAT_ID>"},
	"format_hint_generic":      {EN: "Any https address. It gets a JSON POST: {event, service, group, state, previous, reason, at, url}.", PL: "Dowolny adres https. Dostaje POST z JSON-em: {event, service, group, state, previous, reason, at, url}."},
	// API tokens (SHYG-49)
	"tokens_title":          {EN: "API tokens", PL: "Tokeny API"},
	"tokens_desc":           {EN: "Read-only access for Prometheus, Home Assistant or a script: send Authorization: Bearer <token> to /metrics, /api/status or /api/history. A token cannot change anything and cannot sign in to the panel.", PL: "Dostęp tylko do odczytu dla Prometheusa, Home Assistanta albo skryptu: wyślij Authorization: Bearer <token> na /metrics, /api/status lub /api/history. Token niczego nie zmieni i nie zaloguje do panelu."},
	"tokens_name":           {EN: "Name, e.g. prometheus", PL: "Nazwa, np. prometheus"},
	"tokens_create":         {EN: "Create token", PL: "Utwórz token"},
	"tokens_shown_once":     {EN: "Copy the token now. It is shown only once and cannot be read back — if you lose it, revoke it and create a new one.", PL: "Skopiuj token teraz. Pokazujemy go tylko raz i nie da się go odczytać później — jeśli go zgubisz, odwołaj go i utwórz nowy."},
	"tokens_none":           {EN: "No tokens yet.", PL: "Brak tokenów."},
	"tokens_revoke":         {EN: "Revoke", PL: "Odwołaj"},
	"tokens_confirm_revoke": {EN: "Revoke the token “%s”? Whatever uses it will get 401 from now on.", PL: "Odwołać token „%s”? Wszystko, co go używa, dostanie od teraz 401."},
	"tokens_last_used":      {EN: "last used %s", PL: "ostatnio użyty %s"},
	"tokens_never_used":     {EN: "never used", PL: "nigdy nieużyty"},
	"tokens_created":        {EN: "Token created.", PL: "Token utworzony."},
	"tokens_revoked":        {EN: "Token revoked.", PL: "Token odwołany."},
	"copy":                  {EN: "Copy", PL: "Kopiuj"},
	"copied":                {EN: "Copied", PL: "Skopiowano"},
	"error_token_name":      {EN: "Give the token a name (1–64 characters, no control characters).", PL: "Nazwij token (1–64 znaki, bez znaków sterujących)."},
	"error_token_limit":     {EN: "Too many tokens (50 at most). Revoke one you no longer use.", PL: "Za dużo tokenów (najwyżej 50). Odwołaj któryś, którego już nie używasz."},
	// content check (SHYG-48)
	"chip_content":       {EN: "wrong content", PL: "zła treść"},
	"state_reason":       {EN: "Reason", PL: "Powód"},
	"check_get":          {EN: "GET %s", PL: "GET %s"},
	"check_contains":     {EN: "must contain “%s”", PL: "musi zawierać „%s”"},
	"check_not_contains": {EN: "must not contain “%s”", PL: "nie może zawierać „%s”"},
	// service description and tags (SHYG-47)
	"service_description":             {EN: "Description", PL: "Opis"},
	"service_description_placeholder": {EN: "A short line shown under the name", PL: "Krótka linia pod nazwą"},
	"service_tags":                    {EN: "Tags", PL: "Tagi"},
	"service_tags_placeholder":        {EN: "media, 4k, family", PL: "media, 4k, rodzina"},
	"error_description_long":          {EN: "The description is too long (200 characters at most).", PL: "Opis jest za długi (najwyżej 200 znaków)."},
	"error_description_chars":         {EN: "The description must be a single line without control characters.", PL: "Opis musi być jedną linią, bez znaków sterujących."},
	"error_tags":                      {EN: "Tags: at most 12, each up to 32 characters, without control characters.", PL: "Tagi: najwyżej 12, każdy do 32 znaków, bez znaków sterujących."},
	"error_field_inherited":           {EN: "This value is inherited from a shared block (<<: *defaults), so it cannot be changed here. Edit it in the file.", PL: "Ta wartość jest dziedziczona ze wspólnego bloku (<<: *defaults), więc nie da się jej zmienić tutaj. Zmień ją w pliku."},
	"error_name_duplicate":            {EN: "That name is already taken here. Pick a different one.", PL: "Ta nazwa jest tu już zajęta. Wybierz inną."},
	"error_title_chars":               {EN: "The title must be a single line without control characters.", PL: "Tytuł musi być jedną linią, bez znaków sterujących."},
	"error_service_address":           {EN: "Invalid address: use http://, https:// or tcp://host:port.", PL: "Zły adres: użyj http://, https:// albo tcp://host:port."},
	"banner_error_previous":           {EN: "The config file has an error. The panel keeps running on the last working configuration until it is fixed.", PL: "Plik konfiguracji ma błąd. Panel działa dalej na ostatniej działającej konfiguracji, dopóki go nie poprawisz."},
	"banner_error_history":            {EN: "The config file has an error. The panel is showing the last version saved in history.", PL: "Plik konfiguracji ma błąd. Panel pokazuje ostatnią wersję zapisaną w historii."},
	"banner_error_empty":              {EN: "The config file has an error and there is no earlier version to fall back to — nothing to show until it is fixed.", PL: "Plik konfiguracji ma błąd, a nie ma wcześniejszej wersji, na którą można wrócić — nie ma czego pokazać, dopóki go nie poprawisz."},
	"banner_position":                 {EN: "Line %d, column %d: %s", PL: "Linia %d, kolumna %d: %s"},
	"banner_line":                     {EN: "Line %d: %s", PL: "Linia %d: %s"},
	"banner_warn_title":               {EN: "The config loaded, but part of it is ignored:", PL: "Config się wczytał, ale część jest pomijana:"},
	"banner_warn_key":                 {EN: `unknown key "%s" (%s)`, PL: `nieznany klucz „%s” (%s)`},
	"banner_warn_line":                {EN: "line %d", PL: "linia %d"},
	"banner_warn_suggestion":          {EN: `— did you mean "%s"?`, PL: `— chodziło o „%s”?`},
	"setup_token":                     {EN: "Setup token", PL: "Token instalacji"},
	"setup_token_hint":                {EN: "Printed in the panel's log when it started (for Docker: docker logs <container>). It proves you run this instance.", PL: "Wypisany w logu panelu przy starcie (w Dockerze: docker logs <kontener>). Dowodzi, że to Ty prowadzisz tę instancję."},
	"setup_token_bad":                 {EN: "The setup token is wrong. Copy it from the panel's log.", PL: "Zły token instalacji. Skopiuj go z logu panelu."},
	"invalid_name":                    {EN: "Names must be 1–200 characters long, without control characters or line breaks.", PL: "Nazwa musi mieć 1–200 znaków i nie może zawierać znaków sterujących ani łamania wiersza."},
	"webhook_test_failed":             {EN: "The test message could not be delivered. Check the webhook address; the details are in the panel's log.", PL: "Nie udało się dostarczyć wiadomości testowej. Sprawdź adres webhooka; szczegóły są w logu panelu."},
	"banner_warn_check":               {EN: `service "%s" in group "%s": check.%s is %s, outside 1s–24h; using %s instead`, PL: `usługa „%s” w grupie „%s”: check.%s wynosi %s, poza zakresem 1 s–24 h; używam %s`},
	"banner_warn_credentials":         {EN: `service "%s" in group "%s": the url contains a login and password (%s). It is stored in the config file in plain text; the panel hides it from notifications and the page, but keep it out of git`, PL: `usługa „%s” w grupie „%s”: adres zawiera login i hasło (%s). Leży jawnym tekstem w pliku konfiguracji; panel ukrywa je w powiadomieniach i na stronie, ale nie wrzucaj go do gita`},
	"banner_warn_address":             {EN: `service "%s" in group "%s" has an address that cannot be used: %s (use http://, https:// or tcp://host:port)`, PL: `usługa „%s” w grupie „%s” ma adres, którego nie da się użyć: %s (użyj http://, https:// albo tcp://host:port)`},
	"banner_top_level":                {EN: "top level", PL: "poziom główny"},
	"error_move":                      {EN: "Could not move: invalid group, name or position.", PL: "Nie można przenieść: zła grupa, nazwa albo pozycja."},

	// editing the "Checking" section (GH #6)
	"error_check_duration": {EN: "Interval and timeout take a duration of at least 1s, such as 30s, 2m or 1h.", PL: "Co ile i timeout przyjmują czas od 1s w górę, np. 30s, 2m albo 1h."},
	"error_file_layout":    {EN: "This part of the file is laid out in a way the editor cannot change safely. Nothing was saved — edit it in the file.", PL: "Ta część pliku ma układ, którego edytor nie zmieni bezpiecznie. Nic nie zapisano — zmień to w pliku."},
	"error_http_codes":     {EN: "Accepted codes: HTTP status codes from 100 to 599, separated by commas.", PL: "Akceptowane kody: kody HTTP od 100 do 599, oddzielone przecinkami."},

	"file_conflict": {
		EN: "the file changed on disk since it was loaded — refresh the page and repeat the change",
		PL: "plik zmienił się na dysku od czasu wczytania — odśwież stronę i powtórz zmianę"},
}

// T returns the text in the given language; when a translation is missing it falls back to English.
func T(l Lang, key string) string {
	m, ok := texts[key]
	if !ok {
		return key
	}
	if v, ok := m[l]; ok && v != "" {
		return v
	}
	return m[EN]
}

// Map returns the full set of texts for a language — handy for templates and for JS.
func Map(l Lang) map[string]string {
	out := make(map[string]string, len(texts))
	for k := range texts {
		out[k] = T(l, k)
	}
	return out
}

// Keys returns a sorted list of keys — used in consistency tests.
func Keys() []string {
	out := make([]string, 0, len(texts))
	for k := range texts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FromRequest determines the language: the user's cookie, then Accept-Language,
// and as a last resort English.
func FromRequest(r *http.Request) Lang {
	if c, err := r.Cookie(CookieName); err == nil {
		if l := ParseLang(c.Value); l != "" {
			return l
		}
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		code := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if l := ParseLang(code); l != "" {
			return l
		}
	}
	return EN
}

// ParseLang maps a language code to a supported one; an empty string = unsupported.
func ParseLang(code string) Lang {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case code == "pl" || strings.HasPrefix(code, "pl-"):
		return PL
	case code == "en" || strings.HasPrefix(code, "en-"):
		return EN
	}
	return ""
}
