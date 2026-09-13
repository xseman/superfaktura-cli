# Writing from the browser: a new invoice through the form generated from
# 'sf invoice create', its line items totalled as they are typed, then a
# payment against it.
# Record with: make demo   (needs vhs v0.10 or v0.11, ttyd, ffmpeg and a Chrome)
Output docs/demo/form.gif

Require ttyd
Require go
Require curl

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 18
Set Width 1280
Set Height 800
Set Padding 20
Set TypingSpeed 50ms
Set Framerate 24

Hide
Type "source docs/demo/setup.sh && clear" Enter
Sleep 15s
Type "sf ui" Enter
Sleep 2s
Tab
Sleep 1s
Show

Sleep 1500ms

# n opens the form; it offers what 'sf invoice create --help' does.
Type "n"
Sleep 1500ms
Type "Konzultácie september"
Enter
Sleep 600ms

# A client is picked from the account's list, not typed.
Down
Sleep 500ms
Enter
Sleep 600ms

# Comment, constant symbol and the three dates stay unset.
Enter
Sleep 200ms
Enter
Sleep 200ms
Enter
Sleep 200ms
Enter
Sleep 200ms
Enter
Sleep 800ms

# Line items, one per line, totalled underneath as they are typed.
Type "Konzultácie:8:75:23"
Sleep 1200ms
Ctrl+J
Type "Cestovné:1:40:23"
Sleep 2500ms
Enter
Sleep 500ms

# No new client; a payment type from the fixed set; a tag ticked by name.
Enter
Sleep 500ms
Down
Sleep 700ms
Enter
Sleep 500ms
Down
Sleep 400ms
Type "x"
Sleep 1200ms
Enter
Sleep 500ms

# Document type and variable symbol stay unset, then Create.
Enter
Sleep 300ms
Enter
Sleep 1000ms
Enter
Sleep 3s

# The new invoice is selected. p records a payment; empty pays in full.
Type "p"
Sleep 1500ms
Enter
Sleep 3500ms
