create table files
(
    id         int unsigned auto_increment primary key,
    created_at timestamp            null,
    updated_at timestamp            null,
    deleted_at timestamp            null,
    src        varchar(255)         not null,
    status     tinyint(1) default 1 null,
    profile_id int unsigned         null,
    project_id int unsigned         null
) collate = utf8mb4_general_ci;
