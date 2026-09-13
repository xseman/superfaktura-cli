# Output for scripts: a JSON envelope when piped, --jq, --count, --ids-only,
# and a failure that carries its code and its candidates.
# Record with: make demo   (needs vhs v0.10 or v0.11, ttyd, ffmpeg and a Chrome)
Output docs/demo/output.gif

Require ttyd
Require go
Require curl
Require jq

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

# Piped, the same command is a JSON envelope.
Type "sf invoice list --status 99 | jq -c '.meta, .data[].Client.name'" Enter
Sleep 3s

# Or filter it in place, no jq needed.
Type `sf invoice list --status 99 --jq '.[] | [.Invoice.invoice_no_formatted, ."0".to_pay]' | jq -c` Enter
Sleep 3s

Type "sf invoice list --status 99 --count" Enter
Sleep 1500ms
Type "sf invoice list --status 99 --ids-only" Enter
Sleep 2500ms

# A name that matches two clients fails rather than guesses: exit 8, with
# the candidates to choose from.
Ctrl+L
Type "sf invoice create --client Acme --item 'Consulting:1:500:23' --json; echo exit $?" Enter
Sleep 5s
