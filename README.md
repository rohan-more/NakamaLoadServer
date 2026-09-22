# NakamaLoadServer

Authoritative [Nakama](https://heroiclabs.com/) server for a small two player
shooter, written as a Go runtime plugin. It exists to be load tested: the
gameplay is deliberately minimal so the interesting part is how matchmaking and
match lifecycle behave under many concurrent clients.

Companion repositories:

- [NakamaLoadTester](https://github.com/rohan-more/NakamaLoadTester) — Unity client
- [NakamaLoadBot](https://github.com/rohan-more/NakamaLoadBot) — headless Go bots for load testing

## Running

Requires Docker. The Go plugin is compiled inside the build, so no local Go
toolchain is needed.

```shell
docker compose up -d --build nakama
```

Nakama listens on 7350 (client API) and 7351 ([console](http://127.0.0.1:7351),
default login `admin:password`). Rebuild after changing Go code by rerunning the
same command.

```shell
docker compose down      # stop
docker compose down -v   # stop and wipe the database volume
```

Match state lives in memory, so restarting `nakama` clears all in-flight
matches. Accounts persist in Postgres.

## The match

A match is `maxPlayers = 2`, runs at `tickRate = 10` ticks per second, and each
player starts on `startingHealth = 100`.

- Shooting picks a random living opponent and deals 1–10 damage.
- A player may shoot once per `cooldownTicks = 20` (2 seconds); earlier shots
  are discarded server side.
- The match starts once both slots are filled, and is decided when at most one
  player is left standing.
- Once decided it stays alive for 50 ticks (5 seconds) so clients reliably
  receive the result before Nakama tears it down.
- A match left empty for `idleTicks` (30 seconds) is closed, so abandoned
  matches don't accumulate and get handed back out by matchmaking.

Leaving is a forfeit while the match is running, which drops the leaver to zero
health and resolves the other player as the winner. Leaving before the match
starts frees the slot outright.

### Opcodes

| Opcode | Direction | Payload |
| --- | --- | --- |
| 1 `OpCodeShoot` | client to server | none |
| 2 `OpCodeStateSync` | server to clients | `{"players":[{"user_id","username","health"}],"started":bool}` |
| 3 `OpCodeMatchOver` | server to clients | `{"winner_id","winner_username"}` |

State sync is broadcast when a player joins or leaves and whenever damage is
dealt. On match over, empty winner fields mean a draw, which happens when both
players forfeit.

Players are sent as a list rather than a map keyed by user id because Unity's
`JsonUtility` cannot deserialize a map.

## RPCs

All require an authenticated session.

| RPC | Response | Purpose |
| --- | --- | --- |
| `find_match` | `{"match_id","created"}` | Join an open match, or create one if there isn't a joinable match. `created` reports which happened. |
| `create_match` | `{"match_id"}` | Always create a new match. |
| `add_score` | `{"new_score"}` | Adds points to the caller's stored score. Takes `{"points": n}` where n is 1–100. |
| `rewards` | `{"coins_received"}` | Daily reward, from the upstream template. |

Clients cannot create authoritative matches directly. `socket.CreateMatchAsync()`
makes a *relayed* match with no server handler attached, which silently does
nothing here: no `MatchInit`, no state broadcasts. Creation has to go through
`find_match` or `create_match`, and the client then joins the returned id over
its socket.

### Calling an RPC by hand

```shell
curl "127.0.0.1:7350/v2/account/authenticate/device" --data "{\"id\": \"00000000-0000-0000-0000-000000000001\"}" --user 'defaultkey:'
curl "127.0.0.1:7350/v2/rpc/find_match" -H "Authorization: Bearer $TOKEN" --data '"{}"'
```

The [console API explorer](http://127.0.0.1:7351/apiexplorer) does the same
without the token juggling.

## Matchmaking notes

Two things about `find_match` are worth knowing, because both were found by
load testing rather than by reading the code.

**The match index lags `MatchCreate`.** A match created moments ago is not yet
visible to `MatchList`, so callers arriving together each saw an empty list and
each created their own match instead of pairing. `find_match` serialises the
find-or-create decision and remembers the match it last created, checking it
directly with `MatchGet`.

**A handout is not an arrival.** `find_match` returns an id, but the client
joins over its socket afterwards, so the match still reads as empty while
callers are on their way to it. Without accounting for that, one free slot gets
promised to every caller in a burst and all but one are rejected on arrival.
Each handout now holds a seat for that user, and capacity checks count held
seats alongside players who have already arrived. The seat is released when the
user joins, from which point the match size counts them instead; an unused seat
expires after 10 seconds. Holding it until expiry instead would count a new
arrival twice, and the match would read as full to everyone after them.

### Switching the fixes off

Both fixes can be turned off with `MATCHMAKING_MODE`, so their effect can be
reproduced rather than taken on trust. Each level includes the one before it.

| Mode | Behaviour |
| --- | --- |
| `naive` | List open matches and create one if none are found. Callers arriving together each create their own match. |
| `serialized` | Serialise find-or-create and hand out the last created match directly. Fixes duplicate creation, but a burst still gets one slot promised to many callers. |
| `seats` | Also hold a seat per handout until the player joins. The default. |

```shell
MATCHMAKING_MODE=naive docker compose up -d --force-recreate nakama
```

In PowerShell set it first with `$env:MATCHMAKING_MODE="naive"`. The server
logs `Matchmaking mode: <mode>` at startup and refuses to start on an unknown
value, rather than quietly running a different mode. The ladder runner in
[NakamaLoadBot](https://github.com/rohan-more/NakamaLoadBot) switches modes
automatically and measures each one.

Bots all starting at once, 60 seconds per run, one machine:

| Mode | Bots | Games started | Orphaned matches | Join failures | `find_match` p95 |
| --- | --- | --- | --- | --- | --- |
| `naive` | 100 | 0 | 100 | 0% | 29.0 ms |
| `naive` | 500 | 180 | 190 | 15.0% | 41.6 ms |
| `serialized` | 100 | 96 | 1 | 52.3% | 15.3 ms |
| `serialized` | 500 | 480 | 1 | 47.8% | 51.1 ms |
| `seats` | 100 | 100 | 0 | 0% | 33.8 ms |
| `seats` | 500 | 496 | 1 | 0% | 94.7 ms |

`naive` fails silently: at 100 bots there are no join failures at all, yet no
game starts, because every bot has created and joined its own empty match. It
only shows up as orphaned matches. `serialized` pairs players but rejects about
half of all joins. `seats` pairs everyone with no rejections, at the cost of
`find_match` latency under a burst, since every call waits on the same lock.
With a 100ms stagger between bots all three modes mostly cope.

The full grid, including staggered starts and 20 bot runs, is in
[NakamaLoadBot's benchmarks](https://github.com/rohan-more/NakamaLoadBot/tree/master/benchmarks/2026-09-22-local).
These are localhost figures, bots and server on the same machine.

## Usernames

New accounts are renamed in an after-authenticate hook to an adjective-noun-number
name. The pool is small enough that names collide once there are a few hundred
accounts, so a collision falls back to appending part of the user id, which is
unique by definition. A name that still cannot be set is logged and ignored
rather than failing the authentication that already succeeded.

Note the session token is minted before this hook runs, so `session.Username`
on the client is the pre-rename name. Clients should read the account back
instead.

## Leftovers from the upstream template

This started from [nakama-project-template](https://github.com/heroiclabs/nakama-project-template)
and some of it is still around:

- The Lua and TypeScript runtimes still load and register their own RPCs
  (`reward`, `rewards_js`, `find_match_js`). They are unused by the clients here.
- `docker-compose.yml` still defines a `tf` service and `./model` holds a
  pre-trained model used by the template's tic-tac-toe AI opponent. The Go AI
  code was removed when the match handler became a shooter, so nothing in the
  Go runtime calls it.
