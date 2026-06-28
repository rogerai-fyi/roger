{
  # Reproducible AMD SEV-SNP confidential CVM image for a RogerAI provider node.
  #
  # Goal: bit-identical OVMF + kernel + initrd + dm-verity rootfs from pinned inputs, so the
  # SEV-SNP launch measurement is stable and independently verifiable. Direct boot is used
  # (OVMF + kernel + initrd + cmdline) because it gives sev-snp-measure a clean, complete
  # picture of the measured initial state.
  #
  # SCAFFOLD: the package pins below are marked TODO. Fill them in for your fleet, then run
  # `nix build .#cvm` and the build->measure->verify loop in docs/tee-runbook.md. nixpkgs is
  # pinned by the flake.lock you commit (run `nix flake update` once to generate it).

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.05"; # TODO: pin to a specific rev in flake.lock
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux"; # SEV-SNP guests are amd64 only
      pkgs = import nixpkgs { inherit system; };

      # The kernel command line. The dm-verity root hash MUST be baked in here so the
      # rootfs is measured-by-reference (any rootfs change flips the cmdline -> the
      # measurement). roger.broker can be pinned too (config-as-code; changing it changes
      # the measurement, which is the honest behavior).
      cmdline = "console=ttyS0 root=/dev/dm-0 ro "
        + "roothash=@VERITY_ROOTHASH@ "      # TODO: substituted from the dm-verity build
        + "roger.broker=https://broker.rogerai.fyi "
        + "panic=-1";

      # OVMF firmware built for SEV-SNP. TODO: pin the exact AMDSEV/edk2 build + flags.
      ovmf = pkgs.OVMF.fd; # placeholder; replace with the SEV-SNP-enabled firmware

      # The guest kernel: SEV guest drivers (sev-guest / configfs-tsm) + a minimal config.
      kernel = pkgs.linuxPackages.kernel; # TODO: pin version + a hardened minimal config

      # The in-guest payload baked into the initrd/rootfs: start the model server, then
      # `roger share --confidential`. Read-only, single-purpose, no shell.
      nodeInit = ./node-init.sh;
    in {
      packages.${system}.cvm = pkgs.stdenv.mkDerivation {
        pname = "rogerai-cvm";
        version = "0.1.0-scaffold";
        # TODO: assemble vmlinuz + a dm-verity rootfs.img (with `roger` + the model server +
        # node-init.sh as init) + an initrd that sets up dm-verity from @VERITY_ROOTHASH@.
        # Emit result/{OVMF.fd,vmlinuz,initrd,rootfs.img,cmdline}.
        dontUnpack = true;
        buildInputs = [ pkgs.cryptsetup pkgs.coreutils ];
        installPhase = ''
          mkdir -p $out
          cp ${ovmf} $out/OVMF.fd
          cp ${kernel}/bzImage $out/vmlinuz 2>/dev/null || cp ${kernel}/* $out/vmlinuz
          # TODO: real initrd + dm-verity rootfs build; node-init.sh is the guest init.
          install -m0755 ${nodeInit} $out/node-init.sh
          printf '%s' "${cmdline}" > $out/cmdline
          echo "SCAFFOLD: replace the TODOs in flake.nix with real pinned build steps." >&2
        '';
      };

      # `nix build` default.
      packages.${system}.default = self.packages.${system}.cvm;
    };
}
