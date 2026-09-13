CREATE TABLE job_subscriptions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    guild_id VARCHAR(32) NOT NULL,
    channel_id VARCHAR(32) NOT NULL,
    name VARCHAR(100) NOT NULL,
    search_query TEXT NOT NULL,
    locations_json JSON NOT NULL,
    ai_prompt TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(32) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_job_subscriptions_guild_name (guild_id, name),
    KEY ix_job_subscriptions_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE job_evaluations (
    cache_key CHAR(64) NOT NULL,
    matched BOOLEAN NOT NULL,
    reason TEXT NOT NULL,
    overview TEXT NOT NULL,
    evaluated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (cache_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE job_notifications (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    channel_id VARCHAR(32) NOT NULL,
    linkedin_job_id VARCHAR(128) NOT NULL,
    posted_key VARCHAR(16) NOT NULL,
    status ENUM('pending', 'sent') NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sent_at TIMESTAMP NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_job_notification (channel_id, linkedin_job_id, posted_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
