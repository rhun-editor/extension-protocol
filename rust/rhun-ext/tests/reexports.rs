//! The feature modules' types at the crate root, beside the modules' own paths.

#[test]
fn modules_are_reexported_at_the_root() {
    // the root's Reveal stays the editor's (feature reveal); a task's is TaskReveal and by path
    let _: rhun_ext::Reveal = rhun_ext::editor::Reveal::Center;
    let r: rhun_ext::TaskReveal = rhun_ext::tasks::Reveal::Silent;
    let _: rhun_ext::tasks::Reveal = r;
    let _: Option<rhun_ext::TaskCommand> = None::<rhun_ext::tasks::Command>;
    let _: Option<rhun_ext::NbCommand> = None::<rhun_ext::notebook::Command>;
    let _: rhun_ext::SaveKind = rhun_ext::custom_editor::SaveKind::Backup;
    let _: rhun_ext::Model = rhun_ext::custom_editor::Model::Owned;
    let _: Option<rhun_ext::Notebook> = None::<rhun_ext::notebook::Notebook>;
    let _: Option<rhun_ext::Session> = None::<rhun_ext::ai::Session>;
    let _: Option<rhun_ext::FileQuery> = None::<rhun_ext::files::FileQuery>;
    let _: Option<rhun_ext::Pkce> = None::<rhun_ext::auth::Pkce>;
    let _: Option<rhun_ext::Task> = None::<rhun_ext::tasks::Task>;
    assert_eq!(rhun_ext::save_part(false, b"x"), b"0\nx");
}
