# Changelog

## [0.5.0](https://github.com/NilsRuiz/orbit/compare/agent-v0.4.0...agent-v0.5.0) (2026-11-28)


### Features

* **agent:** issue per-node NATS credentials with tested permissions ([#105](https://github.com/NilsRuiz/orbit/issues/105)) ([a78f6fb](https://github.com/NilsRuiz/orbit/commit/a78f6fb4512f2f81689515d60c202f8b2f9ccafd))

## [0.4.0](https://github.com/NilsRuiz/orbit/compare/agent-v0.3.0...agent-v0.4.0) (2026-11-27)


### Features

* **agent:** deploy payload versions on k3s through a Flux OCIRepository ([#94](https://github.com/NilsRuiz/orbit/issues/94)) ([d143f2a](https://github.com/NilsRuiz/orbit/commit/d143f2ab53beea2f9c4594c9a8f69fdef1eaaafa))

## [0.3.0](https://github.com/NilsRuiz/orbit/compare/agent-v0.2.0...agent-v0.3.0) (2026-11-26)


### Features

* **agent:** send phase, rollout id and labels in heartbeats ([#58](https://github.com/NilsRuiz/orbit/issues/58)) ([b17d938](https://github.com/NilsRuiz/orbit/commit/b17d938f597ea62db943e7207258740a5644a8ff))
* **operator:** add the desired-state store with revert trail ([#63](https://github.com/NilsRuiz/orbit/issues/63)) ([e02ec8d](https://github.com/NilsRuiz/orbit/commit/e02ec8d4c4bbb7dedaacab6ba26a74ecfb3b8fb7)), closes [#46](https://github.com/NilsRuiz/orbit/issues/46)

## [0.2.0](https://github.com/NilsRuiz/orbit/compare/agent-v0.1.0...agent-v0.2.0) (2026-11-24)


### Features

* **agent:** log asynchronous nats errors ([#32](https://github.com/NilsRuiz/orbit/issues/32)) ([3ab650b](https://github.com/NilsRuiz/orbit/commit/3ab650b615ce291c9a77ce8820cb79cce3af7718)), closes [#31](https://github.com/NilsRuiz/orbit/issues/31)


### Bug Fixes

* **agent:** bump nats.go to v1.56.0 to fix kv watch stall ([#36](https://github.com/NilsRuiz/orbit/issues/36)) ([a7c1fb6](https://github.com/NilsRuiz/orbit/commit/a7c1fb6ce94481e9db70a46f60dc7394f9990f6d)), closes [#35](https://github.com/NilsRuiz/orbit/issues/35)
* **agent:** resolve golangci-lint findings and pin linter version ([#20](https://github.com/NilsRuiz/orbit/issues/20)) ([e805340](https://github.com/NilsRuiz/orbit/commit/e805340da204a028c197a624d98670f0694e1f99))
