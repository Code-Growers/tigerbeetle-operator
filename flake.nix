{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/master";
    make-shell.url = "github:nicknovitski/make-shell";
    # Dagger CLI; the version must match `engineVersion` in dagger.json and the CI workflow.
    dagger.url = "github:dagger/nix";
    dagger.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    inputs@{
      self,
      nixpkgs,
      flake-parts,
      systems,
      make-shell,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [ make-shell.flakeModules.default ];
      systems = [
        "x86_64-linux"
        "aarch64-darwin"
      ];

      perSystem =
        {
          config,
          self',
          inputs',
          pkgs,
          system,
          ...
        }:
        {
          make-shells.default = {
            packages = [
              pkgs.go
              pkgs.gopls
              pkgs.golangci-lint
              pkgs.golangci-lint-langserver
              pkgs.opencode

              # Kubernetes / operator tooling
              pkgs.kubebuilder
              pkgs.kubectl
              pkgs.kind
              pkgs.kustomize
              pkgs.tilt
              pkgs.python3 # required by Tilt's helm_resource extension
              pkgs.kubernetes-helm
              pkgs.helm-docs
              pkgs.helmfile
              pkgs.nodejs # docs site (site/, Astro + Starlight)

              # Pipelines (local and CI)
              inputs'.dagger.packages.dagger
            ];
          };
        };
    };
}
