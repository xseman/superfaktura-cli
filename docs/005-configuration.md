# 005 · Configuration and credentials

`internal/config`. Profiles, credential precedence, secret storage.

## Precedence

Later wins. Each layer only overrides values it actually sets, so a profile can
supply the email while the environment supplies the key.

```
  ┌─────────────────────────────────────────────┐
  │ 4  flags        --api-url --company         │  highest
  │                 --module   --profile        │
  ├─────────────────────────────────────────────┤
  │ 3  environment  SF_API_URL  SF_EMAIL        │
  │                 SF_APIKEY   SF_COMPANY_ID   │
  │                 SF_MODULE   SF_PROFILE      │
  ├─────────────────────────────────────────────┤
  │ 2  profile      the named one, or SF_PROFILE│
  │                 or the stored default       │
  ├─────────────────────────────────────────────┤
  │ 1  defaults     BaseURL = moja.superfaktura │  lowest
  └─────────────────────────────────────────────┘
```

There is no global `--email` or `--api-key`: an invocation picks an account by
`--profile`, and those two exist only on `sf auth login`, which is where an
account is defined rather than chosen. The environment carries them because a
CI job has no profile to point at.

**`company_id` matters more than it looks.** The rate limit is counted per
company, so a wrong value does not fail loudly — it spends a different
company's allowance. See [001 · API integration](001-api-integration.md).

## Where secrets live

```
   sf auth login
        │
        ▼
  ┌──────────────────────┐  works   ┌────────────────────────────┐
  │ system keyring       ├─────────►│ service: superfaktura-cli  │
  │ (zalando/go-keyring) │          │ key:     profile:<name>    │
  └──────────┬───────────┘          └────────────────────────────┘
             │ unavailable
             ▼                       ┌────────────────────────────┐
  ┌──────────────────────┐           │ mode 0600, written         │
  │ file fallback        ├──────────►│ atomically                 │
  └──────────────────────┘           │ + a warning that the key   │
                                     │   is stored in plaintext   │
                                     └────────────────────────────┘
```

`SF_NO_KEYRING=1` forces the file path — which is what the tests use.

Everything non-secret (base URL, email, company, module, which profile is
default) lives in the profile file. The API key is the only thing that goes to
the keyring.

## Profiles

```sh
sf auth login sandbox --default   # store one, and make it the default
sf auth list                      # show them, marking the default
sf auth switch sandbox            # change the default
sf auth status                    # verify credentials + show quota
sf auth logout sandbox            # forget one
sf doctor                         # check every profile, not only the active one
```

The profile name is a positional argument, not a flag — `SURFACE.txt` is the
authority on the shape of any command quoted here.

`sf auth login` honours `SF_API_URL`. Without that it would silently save the
production URL under a profile named "sandbox" — which is how that bug was
found.

**Signing in again opens on what is already stored.** Correcting one field
should not mean retyping four, and above all not pasting the API key again to
change a company id. Flags and the environment still win over the saved
profile: they were given for this invocation, the profile was not.

The key is the exception — it is never put back in the field. An empty box
means "keep the one stored", which the form says, and nothing is gained by
loading a secret into a widget when leaving it alone already means unchanged.
With no key stored the field stays required.

`sf auth status` and `sf doctor` are the two commands that may print their
result **and** exit non-zero. They wrap the failure in `reported{}` so
`Execute` sets the code without printing a second envelope. See
[002 · CLI](002-cli.md).

## What `sf doctor` checks, and what it costs

`sf auth status` answers "does *this* invocation work". The mistake that hurts
is in the profile nobody is looking at: the limit is counted per `company_id`
([001 · API integration](001-api-integration.md)), so a company that is wrong,
missing, or shared between two profiles never fails — it spends an allowance
somebody else is relying on. `sf doctor` puts every profile's instance, company
and remaining quota on adjacent lines for exactly that reason.

```
  per run          Store · Environment · Default profile · Quota sharing
  per profile      Instance · Email · API key · Company   ← config only
                   API · Quota                            ← --live only
```

**Nothing is sent to the API without `--live`.** A sweep that spent one request
per profile on every run would be committing the fault it exists to find — and
for a profile whose company id is wrong, spending it against a company that
never asked. `--live` costs exactly one request per profile that has
credentials to try, and says so in its own help.

The exit code is the code of the first failing check, so a profile with no key
exits `auth_required` (3) rather than a generic failure. Warnings — no company
set, a key in the plaintext fallback, two profiles sharing a company — do not
change the exit code; they are conditions to know about, not breakage.

## Paths

| What            | Where                                     | Fallback                                               |
| --------------- | ----------------------------------------- | ------------------------------------------------------ |
| profiles        | `$SF_CONFIG_DIR/config.json`              | `$XDG_CONFIG_HOME/sf` → `~/.config/sf`                 |
| disk cache      | `$SF_CONFIG_DIR/cache`                    | `$XDG_STATE_HOME/sf/cache` → `~/.local/state/sf/cache` |
| secret fallback | alongside the profiles, mode 0600         | —                                                      |
| e2e credentials | `.env.test` at the repo root (gitignored) | environment wins                                       |

The cache sits under `XDG_STATE_HOME`, not beside the config: cached data is
derived, not authored, and deleting it must never cost a profile.

`SF_CONFIG_DIR` exists so tests get a throwaway directory and never touch a real
profile. It moves the cache with it, so a test cannot poison a real one either.
