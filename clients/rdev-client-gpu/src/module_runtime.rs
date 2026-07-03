use std::{future::Future, thread};

#[derive(Clone)]
pub struct ModuleRuntime {
    handle: tokio::runtime::Handle,
}

impl ModuleRuntime {
    pub fn spawn<F>(&self, future: F)
    where
        F: Future<Output = ()> + Send + 'static,
    {
        self.handle.spawn(future);
    }
}

/// Start a dedicated OS thread with a single-thread Tokio runtime for one module class.
pub fn spawn_runtime(name: impl Into<String>) -> ModuleRuntime {
    let name = name.into();
    let (tx, rx) = std::sync::mpsc::channel();
    thread::Builder::new()
        .name(name.clone())
        .spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .thread_name(name)
                .build()
                .expect("module tokio runtime");
            let handle = runtime.handle().clone();
            let _ = tx.send(handle);
            runtime.block_on(std::future::pending::<()>());
        })
        .expect("spawn module runtime thread");
    ModuleRuntime {
        handle: rx.recv().expect("module runtime handle"),
    }
}

/// Run one long-lived module future on its own OS thread with a single-thread Tokio runtime.
pub fn spawn_tokio_thread<F>(name: impl Into<String>, future: F) -> thread::JoinHandle<()>
where
    F: Future<Output = ()> + Send + 'static,
{
    let name = name.into();
    thread::Builder::new()
        .name(name.clone())
        .spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .thread_name(name)
                .build()
                .expect("module tokio runtime");
            runtime.block_on(future);
        })
        .expect("spawn module runtime thread")
}
