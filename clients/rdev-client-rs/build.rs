fn main() {
    if std::env::var("TARGET").as_deref() == Ok("x86_64-pc-windows-gnu") {
        // GNU ld can discard raw-dylib import descriptors while retaining their
        // IAT slots. Windows then jumps into an unresolved RVA before main.
        // Keep the GCC ABI/runtime, but let LLD construct the PE import tables.
        println!("cargo:rustc-link-arg=-fuse-ld=lld");
    }
}
