# HANDOFF - estado del proyecto

Última actualización: 2026-11-14.

## Hecho

Fases del runbook (§17):

| Fase | Estado |
|---|---|
| 0 Preparación | Hecha |
| 1 MVP mock | Hecha: UI táctil tv2beat, eventos/cámaras, máquina de estados, SQLite, copy + verify SHA-256, SSE, historial, `/health` |
| 2 Robustez local | Hecha: cancelar, reintentar, reanudar tras reinicio, colisión de destino, tarjeta retirada/vaciada durante la ingesta. Deduplicación aplazada (ver abajo) |
| 4 Docker (solo mock) | Compose y Dockerfile funcionando en mock; reanudación probada matando el contenedor (agosto) |
| 3, 5-9 | Sin empezar: necesitan hardware |

Historial: los 4 primeros commits (8 de agosto de 2026) se hicieron con Sonnet 5.
El 14 de noviembre de 2026 (Opus 5.5) se revisó el estado y se cerró la fase 2:

- Tests de tarjeta retirada a mitad de copia (`copyengine` y `ingest`), el
  caso que faltaba del backlog del runbook (§23.10).
- **Bug corregido:** una tarjeta que desaparecía dejando un directorio vacío
  (por ejemplo, un punto de montaje tras desenchufar) llegaba a
  `SAFE_TO_EJECT` con 0 archivos copiados, porque `verify` solo compara lo
  que hay en el origen en ese momento. Ahora, si se copian menos archivos de
  los detectados, la ingesta termina en `ERROR`
  (`Manager.runCopyAndVerify`).
- `AGENTS.md`, `CLAUDE.md` y este documento.

## Decisiones pendientes

- **Deduplicación de tarjetas (aplazada a la fase 3, por decisión del
  usuario).** Hoy `scanOnce` ignora para siempre cualquier `source_identifier`
  que ya tenga un job en la base de datos, sea cual sea su estado, y no se
  detecta cuándo se retira una tarjeta. En mock el identificador es el nombre
  del directorio, así que no molesta. Con hardware real, una tarjeta
  reutilizada que conserve el mismo identificador podría no aparecer nunca.
  Hay que decidirlo cuando sepamos qué identificador estable dan
  udev/UGOS (UUID del volumen, serie del lector, by-path). Opciones que se
  barajaron: "reaparecer tras ausencia = tarjeta nueva", con o sin aviso de
  "ya ingerida" por huella de contenido.
- `diskspace` mide `destination_root` como ruta local; puede cambiar con el
  montaje real del NAS.

## Siguiente paso: fase 3, USB real en WSL2

Requisitos: un lector USB y una SD/USB de prueba desechable (nunca material
único). Pasos (runbook §13 y §17):

1. `usbipd-win`: bind y attach del lector a WSL; anotar `lsusb`, `lsblk`,
   `/dev/disk/by-path`, `blkid` con la tarjeta insertada y retirada.
2. Decidir el identificador estable y, con él, la deduplicación.
3. Implementar `internal/device/linux.go` (hoy es un stub) detrás de
   `device.Provider`: detección por polling o udev, montaje de solo lectura,
   umount en `EJECT`.
4. Validar el criterio §18.2: desconexión durante `COPY` termina en `ERROR`.
   El caso del directorio vacío ya está cubierto en mock.

Lo específico de UGOS (estrategia A/B, `SYS_ADMIN`, rutas `/volume1`) espera a
la prueba en el NAS (fase 5, runbook §21).
