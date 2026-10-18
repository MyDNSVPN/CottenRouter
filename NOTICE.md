# Compatibility notice

CottenRouter interoperates with the public wire formats and configuration
schemas of CottenDNS, MasterDnsVPN, StormDNS, thefeed, SlipGate, and Soar. No upstream
binaries or source trees are vendored in this repository. Each upstream project
retains its own copyright and license. Installers and Go source are fetched at
a resolved commit; the Soar 2.2 option resolves WhiteDNS's `v0.1.3` tag and
downloads its pinned release bundle. Downloads happen only when requested by the operator.

Soar is developed by Lantern and licensed under Apache-2.0. Its client/server
dependency is isolated in the integration test module; the routing binary does
not embed Soar. Resolver data distributed by Soar has its own third-party notices.

The **Soar 2.2** option uses the older Lantern Soar engine **v0.2.2** from
[WhiteDNS/Soar-Server standalone setup 0.1.3](https://github.com/WhiteDNS/Soar-Server/releases/tag/v0.1.3).
WhiteDNS develops that installer and distribution under Apache-2.0; the engine
retains Lantern's authorship and Apache-2.0 license. The WhiteDNS bundle includes
the original runtime, dependency, and resolver-data license notices. CottenRouter
uses its own service/configuration lifecycle for this option; WhiteDNS's native
standalone manager and optional management panel are not installed.

SlipGate compatibility includes importing enabled DNS-tunnel entries and
answering SlipNet's authenticated reachability/MTU probe. Non-DNS SlipGate
features continue to run in SlipGate itself.
