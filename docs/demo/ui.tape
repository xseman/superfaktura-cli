# The browser: the overview, invoices with their detail, the server-side scope
# and period, a filter over the loaded page, expenses and clients.
# Record with: make demo   (needs vhs v0.10 or v0.11, ttyd, ffmpeg and a Chrome)
Output docs/demo/ui.gif

Require ttyd
Require go
Require curl

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 18
Set Width 1280
Set Height 800
Set Padding 20
Set TypingSpeed 60ms
Set Framerate 24

Hide
Type "source docs/demo/setup.sh && clear" Enter
Sleep 15s
Show

Type "sf ui" Enter
Sleep 2500ms

# What needs attention, and which records each figure covers.
Down
Sleep 1200ms
Down
Sleep 1200ms

# Invoices. The detail under the list is the row already loaded: no request.
Tab
Sleep 2s
Down
Sleep 700ms
Down
Sleep 700ms
Down
Sleep 1200ms

# The whole screen for one record.
Enter
Sleep 3s
Escape
Sleep 800ms

# A filter over the loaded page costs nothing.
Type "/"
Sleep 500ms
Type "acme"
Sleep 800ms
Enter
Sleep 2500ms
Escape
Sleep 1s

# Scope and period are server-side, one request each.
Type "f"
Sleep 2s
Type "t"
Sleep 2500ms

# Expenses and clients.
Tab
Sleep 2500ms
Tab
Sleep 2500ms
