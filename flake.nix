{
  description = "Alborz package and NixOS service";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      each = nixpkgs.lib.genAttrs systems;
    in {
      packages = each (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          # go.mod asks for Go 1.27.3; nixpkgs' default Go may lag behind.
          alborz = pkgs.buildGo127Module {
            pname = "alborz";
            version = self.shortRev or "dirty";
            src = self;
            # The first `nix build` fails and prints the real hash.
            vendorHash = pkgs.lib.fakeHash;
            subPackages = [ "cmd/alborz" ];
            env.CGO_ENABLED = 0;
            meta = {
              description = "Privacy-conscious webmail, calendar, contacts, tasks and journal";
              homepage = "https://git.mehdix.org/alborz";
              license = pkgs.lib.licenses.mit;
              mainProgram = "alborz";
            };
          };
        in { inherit alborz; default = alborz; });

      nixosModules.default = { config, lib, pkgs, ... }:
        let cfg = config.services.alborz;
        in {
          options.services.alborz = {
            enable = lib.mkEnableOption "Alborz";
            listen = lib.mkOption {
              type = lib.types.str;
              default = "127.0.0.1:1323";
            };
            publicUrl = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
            };
            environmentFile = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "Runtime file with LBRZ_* overrides; never a Nix store path.";
            };
          };
          config = lib.mkIf cfg.enable {
            systemd.services.alborz = {
              description = "Alborz webmail and personal information manager";
              wantedBy = [ "multi-user.target" ];
              wants = [ "network-online.target" ];
              after = [ "network-online.target" ];
              environment.XDG_CACHE_HOME = "/var/cache";
              serviceConfig = {
                DynamicUser = true;
                StateDirectory = "alborz";
                StateDirectoryMode = "0700";
                CacheDirectory = "alborz";
                WorkingDirectory = "/var/lib/alborz";
                ExecStart = lib.escapeShellArgs ([
                  (lib.getExe self.packages.${pkgs.stdenv.hostPlatform.system}.default)
                  "-config" "/var/lib/alborz/config.toml"
                  "-data-dir" "/var/lib/alborz"
                  "-addr" cfg.listen
                ] ++ lib.optionals (cfg.publicUrl != null) [ "-public-url" cfg.publicUrl ]);
                Restart = "on-failure";
                UMask = "0077";
                NoNewPrivileges = true;
                PrivateTmp = true;
                ProtectSystem = "strict";
                ProtectHome = true;
              } // lib.optionalAttrs (cfg.environmentFile != null) {
                EnvironmentFile = cfg.environmentFile;
              };
            };
          };
        };
    };
}
