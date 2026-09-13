# The CLI: what is overdue, an invoice planned with --dry-run, issued and paid.
# Record with: make demo   (needs vhs v0.10 or v0.11, ttyd, ffmpeg and a Chrome)
Output docs/demo/cli.gif

Require ttyd
Require go
Require curl

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 18
Set Width 1280
Set Height 600
Set Padding 20
Set TypingSpeed 40ms
Set Framerate 24

Hide
Type "source docs/demo/setup.sh && clear" Enter
Sleep 15s
Show

# What is overdue, as a table on a terminal.
Type "sf invoice list --status 99" Enter
Sleep 3s

# A write, planned: the client name is already resolved to its ID.
Ctrl+L
Type "sf --dry-run invoice create --client 'Acme s.r.o.' --item 'Konzultácie:8:75:23' --due '+14 days'" Enter
Sleep 4500ms

# The same invoice, sent.
Ctrl+L
Type "sf invoice create --client 'Acme s.r.o.' --item 'Konzultácie:8:75:23' --due '+14 days'" Enter
Sleep 3s

# Paid in full, and what it looks like now.
Type "sf invoice pay 309111" Enter
Sleep 1500ms
Ctrl+L
Type "sf invoice view 309111" Enter
Sleep 5s
