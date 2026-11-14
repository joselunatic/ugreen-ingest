# AGENTS.md

Guía para agentes (Claude Code, Codex) que trabajen en este repo. Léela
entera antes de tocar nada; luego `docs/HANDOFF.md` para saber en qué punto
está el trabajo.

## Qué es

Appliance de ingesta de tarjetas SD/CFexpress para Moy Santana (tv2beat),
pensada para correr en Docker sobre un UGREEN NASync DXP4800 Pro. Un
monolito Go: detección de tarjetas, máquina de estados, copia + verificación
por hash, SQLite, REST + SSE y una UI táctil embebida para tablet.

- **Spec:** `docs/RUNBOOK.md` (manda en requisitos y en lo que NO hay que hacer,
  sección 22).
- **Cómo se ejecuta y decisiones tomadas:** `README.md`.
- **Estado actual y siguiente paso:** `docs/HANDOFF.md`. Actualízalo al cerrar
  una tarea significativa.

## Dónde vive el código

- El repo está en el filesystem de WSL2: `~/repos/ugreen-ingest` (Ubuntu).
  Desde Windows: `\\wsl.localhost\Ubuntu\home\jose\repos\ugreen-ingest`.
  No trabajar en una copia bajo `/mnt/c` (runbook §12).
- `C:\Users\JoseAntonioHernandez\Repos\ugreen-ingest` en Windows NO es el repo:
  solo contiene el runbook original (`.md` y `.docx`).

## Comandos

Go está en `~/.local/go/bin`, que solo entra en el PATH de shells
interactivos (`.bashrc`). Desde un agente o script:

```bash
export PATH=$HOME/.local/go/bin:$PATH
go vet ./...
go test -count=1 ./...
go run ./cmd/ingest                                     # mock, :8080
docker compose -f compose.yml -f compose.dev.yml up --build   # mock, :8088
./scripts/create-mock-card.sh CARD_X                    # simula insertar tarjeta
```

## Reglas del proyecto

- **Nunca** alcanzar `SAFE_TO_EJECT` sin una verificación correcta previa; nunca
  borrar ni formatear la tarjeta de origen. Formatear sigue en `501`.
- Simplicidad (runbook §2 y §22): nada de microservicios, Redis, Postgres, apps
  nativas, ni rclone/MHL en el camino crítico.
- Todo lo que dependa de hardware real (USB, UGOS) va detrás de
  `device.Provider` y se marca como pendiente de validación física.
- Test primero en lógica de dominio y bugs (`internal/ingest`, `copyengine`,
  `verify`, `db`). Los tests de cancelar/retirar tarjeta dependen de tiempos con
  fixtures grandes; si se vuelven intermitentes, arréglalos sin debilitarlos.
- Commits pequeños con prefijo convencional (`feat:`, `fix:`, `test:`,
  `docs:`...). No hacer commit ni push sin que lo pida el usuario.
- `dev/cards/*` y `dev/data/` están en `.gitignore`: no subir media de prueba.
