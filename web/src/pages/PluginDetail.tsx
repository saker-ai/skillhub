import { useState, useEffect } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { getPlugin, getPluginVersions, getPluginFile, type Plugin, type PluginVersion } from '../api/plugins';

export default function PluginDetail() {
  const { slug } = useParams<{ slug: string }>();
  const { t } = useTranslation();
  const [plugin, setPlugin] = useState<Plugin | null>(null);
  const [versions, setVersions] = useState<PluginVersion[]>([]);
  const [manifest, setManifest] = useState<Record<string, unknown> | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);

  useEffect(() => {
    if (!slug) return;
    setLoading(true);
    Promise.all([
      getPlugin(slug).catch(() => null),
      getPluginVersions(slug).then(r => r.versions).catch(() => []),
      getPluginFile(slug, 'latest', 'plugin.json')
        .then(r => r.ok ? r.json() : null)
        .catch(() => null),
    ]).then(([p, v, m]) => {
      if (!p) {
        setNotFound(true);
      } else {
        setPlugin(p);
        setVersions(v || []);
        setManifest(m);
      }
      setLoading(false);
    });
  }, [slug]);

  if (loading) {
    return (
      <div className="container" style={{ padding: '64px 0', textAlign: 'center' }}>
        <div className="spinner" />
      </div>
    );
  }

  if (notFound || !plugin) {
    return (
      <div className="container" style={{ padding: '64px 0', textAlign: 'center', color: 'var(--text-muted)' }}>
        {t('plugin_detail.not_found')}
      </div>
    );
  }

  const title = plugin.displayName || plugin.slug;
  const latestVersion = versions.find(v => !v.yankedAt);
  const basePath = (import.meta.env.BASE_URL || '/').replace(/\/$/, '');
  const fmt = (d: string) => new Date(d).toLocaleDateString();
  const initial = plugin.ownerHandle?.[0]?.toUpperCase() || '?';

  return (
    <section style={{ padding: '40px 0' }}>
      <div className="container">
        <div className="detail-header">
          <div className="owner-info" style={{ marginBottom: 12 }}>
            <div className="owner-avatar">{initial}</div>
            <span style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{plugin.ownerHandle}</span>
          </div>
          <h1 className="detail-title">{title}</h1>
          <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)', marginBottom: 12, fontFamily: 'monospace' }}>
            {plugin.ownerHandle}/{plugin.slug}
          </div>
          {plugin.summary && <p className="detail-summary">{plugin.summary}</p>}
          <div className="detail-stats">
            <div className="stat"><span className="stat-value">{plugin.starsCount}</span><span className="stat-label">{t('plugin_detail.stars')}</span></div>
            <div className="stat"><span className="stat-value">{plugin.downloads}</span><span className="stat-label">{t('plugin_detail.downloads')}</span></div>
            <div className="stat"><span className="stat-value">{versions.length}</span><span className="stat-label">{t('plugin_detail.versions')}</span></div>
          </div>
          {plugin.tags && plugin.tags.length > 0 && (
            <div className="card-tags">
              {plugin.tags.map(tag => <span key={tag} className="tag">{tag}</span>)}
            </div>
          )}
        </div>

        <div className="detail-layout">
          <div>
            {manifest ? (
              <>
                {Array.isArray((manifest as Record<string, unknown>).skills) && ((manifest as Record<string, unknown>).skills as Record<string, unknown>[]).length > 0 && (
                  <section style={{ marginBottom: 24 }}>
                    <h2 style={{ fontSize: '1.1rem', marginBottom: 12 }}>{t('plugin_detail.skills_included')}</h2>
                    <ul className="version-list">
                      {((manifest as Record<string, unknown>).skills as string[]).map((s: string) => (
                        <li key={s} className="version-item"><code>{s}</code></li>
                      ))}
                    </ul>
                  </section>
                )}
                {manifest.mcp_servers && typeof manifest.mcp_servers === 'object' && Object.keys(manifest.mcp_servers as object).length > 0 && (
                  <section style={{ marginBottom: 24 }}>
                    <h2 style={{ fontSize: '1.1rem', marginBottom: 12 }}>{t('plugin_detail.mcp_servers')}</h2>
                    <ul className="version-list">
                      {Object.entries(manifest.mcp_servers as Record<string, Record<string, string>>).map(([name, srv]) => (
                        <li key={name} className="version-item"><code>{name}</code> — {srv.type || 'stdio'}</li>
                      ))}
                    </ul>
                  </section>
                )}
                <section>
                  <h2 style={{ fontSize: '1.1rem', marginBottom: 12 }}>{t('plugin_detail.manifest')}</h2>
                  <pre className="code-block" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{JSON.stringify(manifest, null, 2)}</pre>
                </section>
              </>
            ) : (
              <div style={{ color: 'var(--text-muted)', padding: '40px 0', textAlign: 'center' }}>
                {t('plugin_detail.no_manifest')}
              </div>
            )}
          </div>

          <div>
            <div className="detail-sidebar">
              <div className="sidebar-section">
                <div className="sidebar-label">{t('plugin_detail.install')}</div>
                <div className="code-block" style={{ padding: '10px 14px', fontSize: '0.8rem', fontFamily: 'monospace' }}>
                  npx skillhub install-plugin {plugin.slug}
                </div>
              </div>

              {latestVersion && (
                <div className="sidebar-section">
                  <div className="sidebar-label">{t('plugin_detail.latest_version')}</div>
                  <div className="sidebar-value" style={{ fontFamily: 'monospace', color: 'var(--accent)' }}>v{latestVersion.version}</div>
                </div>
              )}

              <div className="sidebar-section">
                <div className="sidebar-label">{t('plugin_detail.published_by')}</div>
                <div className="owner-info">
                  <div className="owner-avatar">{initial}</div>
                  <span className="sidebar-value">{plugin.ownerHandle}</span>
                </div>
              </div>

              <div className="sidebar-section">
                <div className="sidebar-label">{t('plugin_detail.created')}</div>
                <div className="sidebar-value">{fmt(plugin.createdAt)}</div>
              </div>

              <div className="sidebar-section">
                <div className="sidebar-label">{t('plugin_detail.updated')}</div>
                <div className="sidebar-value">{fmt(plugin.updatedAt)}</div>
              </div>

              {latestVersion && (
                <div className="sidebar-section">
                  <a
                    href={`${basePath}/api/v1/plugins/download?slug=${plugin.slug}&version=${latestVersion.version}`}
                    className="btn btn-secondary"
                    style={{ width: '100%', justifyContent: 'center' }}
                  >
                    &#8615; {t('plugin_detail.download_zip')}
                  </a>
                </div>
              )}
            </div>

            {versions.length > 0 && (
              <div className="detail-sidebar" style={{ marginTop: 16 }}>
                <div className="sidebar-label" style={{ marginBottom: 12 }}>{t('plugin_detail.version_history')}</div>
                <ul className="version-list">
                  {versions.map(v => (
                    <li className="version-item" key={v.id}>
                      <span className="version-tag">v{v.version}</span>
                      <span className="version-date">{fmt(v.createdAt)}</span>
                      {v.yankedAt && <span className="tag" style={{ background: 'var(--danger, #dc3545)', color: '#fff' }}>yanked</span>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}
