{
  description = "osrs-login — minimal Jagex-account launcher for Old School RuneScape";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: {
        default = pkgs.buildGoModule {
          pname = "osrs-login";
          version = "0.1.0";
          src = ./.;
          vendorHash = null; # no external dependencies

          postInstall = ''
            install -Dm644 -t $out/share/applications dist/*.desktop
          '';

          meta = {
            description = "Minimal Bolt-free Jagex-account launcher for OSRS (browser login + RuneLite handoff)";
            mainProgram = "osrs-login";
            license = nixpkgs.lib.licenses.mit;
            platforms = systems;
          };
        };
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go ]; };
      });
    };
}
