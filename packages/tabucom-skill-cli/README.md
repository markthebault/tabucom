# @tabucom/skill

Install and configure the Tabucom Agent Skill without putting credentials in a
skill file, shell profile, or repository.

```sh
npx @tabucom/skill install --base-url https://tabucom.example.com --agent codex
```

Configuration is stored at `${XDG_CONFIG_HOME:-$HOME/.config}/tabucom/config.json`
with owner-only permissions where the platform supports them. Only the public
service origin is written there. Keep `TABUCOM_PUBLISH_API_KEY`,
`TABUCOM_PUBLISH_TOKEN`, and `TABUCOM_VISITOR_PASSWORD` in your environment or
secret manager.

```sh
npx @tabucom/skill configure --base-url http://localhost:8080
npx @tabucom/skill status
npx @tabucom/skill update --agent codex --global
```

Without `--agent`, `update` first uses `npx skills update tabucom --yes` for the
chosen scope. If the skill is not recorded in the `skills` lock file, it
idempotently runs the same `skills add` command as installation. With `--agent`,
it uses that idempotent add path because the upstream update command has no
agent filter. Configuration is never overwritten.
