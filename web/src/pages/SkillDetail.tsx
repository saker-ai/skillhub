import { useEffect, useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { marked } from 'marked';
import DOMPurify from 'dompurify';
import { getSkill, getVersions, getFile, skillDownloadURL, type Skill, type SkillVersion } from '../api/skills';
import { ApiError } from '../api/client';
import CodeBlock from '../components/CodeBlock';
import { formatDisplayName } from '../utils/displayName';

interface AmbiguousCandidate {
  namespace: string;
  slug: string;
  ownerHandle: string;
  skillId: string;
}

marked.setOptions({ breaks: true, gfm: true });

function stripFrontmatter(text: string): string {
  const match = text.match(/^---\s*\n[\s\S]*?\n---\s*\n/);
  return match ? text.slice(match[0].length) : text;
}

type DocTab = 'readme' | 'skill';

export default function SkillDetail() {
  const { t } = useTranslation();
  const { slug, namespace } = useParams<{ slug: string; namespace?: string }>();
  const ns = namespace?.replace(/^@/, '');
  const [skill, setSkill] = useState<Skill | null>(null);
  const [versions, setVersions] = useState<SkillVersion[]>([]);
  const [skillMd, setSkillMd] = useState('');
  const [readmeMd, setReadmeMd] = useState('');
  const [activeTab, setActiveTab] = useState<DocTab>('skill');
  const [notFound, setNotFound] = useState(false);
  const [candidates, setCandidates] = useState<AmbiguousCandidate[]>([]);

  useEffect(() => {
    if (!slug) return;
    setCandidates([]);
    getSkill(slug, ns)
      .then(r => setSkill(r))
      .catch((err) => {
        if (err instanceof ApiError && err.status === 409 && Array.isArray(err.body.candidates)) {
          setCandidates(err.body.candidates as AmbiguousCandidate[]);
        } else {
          setNotFound(true);
        }
      });
    getVersions(slug, ns)
      .then(r => setVersions(r.versions ?? []))
      .catch(e => console.error('Failed to load skill data:', e));

    getFile(slug, 'latest', 'SKILL.md', ns)
      .then(async res => {
        if (!res.ok) return;
        setSkillMd(stripFrontmatter(await res.text()));
      })
      .catch(e => console.error('Failed to load SKILL.md:', e));
    getFile(slug, 'latest', 'README.md', ns)
      .then(async res => {
        if (!res.ok) return;
        setReadmeMd(await res.text());
        setActiveTab('readme');
      })
      .catch(() => { /* README.md is optional */ });
  }, [slug, ns]);

  if (candidates.length > 0) {
    return (
      <section style={{ padding: '40px 0' }}>
        <div className="container" style={{ maxWidth: 640, margin: '0 auto' }}>
          <h2 style={{ marginBottom: 16 }}>Ambiguous skill name</h2>
          <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>
            The slug <strong>{slug}</strong> exists in multiple namespaces. Select one:
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {candidates.map(c => (
              <Link key={c.skillId} to={`/skills/@${c.namespace}/${c.slug}`}
                style={{ padding: '16px 20px', border: '1px solid var(--border)', borderRadius: 8, textDecoration: 'none', color: 'inherit' }}>
                <div style={{ fontWeight: 600, fontFamily: 'monospace' }}>@{c.namespace}/{c.slug}</div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)', marginTop: 4 }}>by {c.ownerHandle}</div>
              </Link>
            ))}
          </div>
        </div>
      </section>
    );
  }

  if (notFound) {
    return (
      <div className="container" style={{ padding: '64px 0', textAlign: 'center', color: 'var(--text-muted)' }}>
        {t('detail.not_found')}
      </div>
    );
  }

  if (!skill) {
    return (
      <div className="container" style={{ padding: '64px 0', textAlign: 'center', color: 'var(--text-muted)' }}>
        {t('common.loading')}
      </div>
    );
  }

  const latestVersion = versions.length > 0 ? versions[0] : null;
  const initial = skill.ownerHandle?.[0]?.toUpperCase() || '?';
  const fmt = (d: string) => new Date(d).toLocaleDateString();
  const title = formatDisplayName(skill.displayName, skill.slug);

  return (
    <section style={{ padding: '40px 0' }}>
      <div className="container">
        <div className="detail-header">
          <div className="owner-info" style={{ marginBottom: 12 }}>
            <div className="owner-avatar">{initial}</div>
            <span style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{skill.ownerHandle}</span>
          </div>
          <h1 className="detail-title" title={title.tooltip}>
            {title.text}
          </h1>
          <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)', marginBottom: 12, fontFamily: 'monospace' }}>
            @{skill.namespaceSlug || skill.ownerHandle}/{skill.slug}
          </div>
          {skill.summary && <p className="detail-summary">{skill.summary}</p>}
          <div className="detail-stats">
            <div className="stat"><span className="stat-value">{skill.starsCount}</span><span className="stat-label">{t('detail.stars')}</span></div>
            <div className="stat"><span className="stat-value">{skill.downloads}</span><span className="stat-label">{t('detail.downloads')}</span></div>
            <div className="stat"><span className="stat-value">{skill.installs}</span><span className="stat-label">{t('detail.installs')}</span></div>
            <div className="stat"><span className="stat-value">{skill.versionsCount}</span><span className="stat-label">{t('detail.versions')}</span></div>
          </div>
          {skill.tags?.length > 0 && (
            <div className="card-tags">
              {skill.tags.map(tag => <span className="tag" key={tag}>{tag}</span>)}
            </div>
          )}
        </div>

        <div className="detail-layout">
          <div>
            {(skillMd || readmeMd) && (
              <div className="doc-tabs" style={{ display: 'flex', gap: 8, borderBottom: '1px solid var(--border)', marginBottom: 16 }}>
                {readmeMd && (
                  <button
                    type="button"
                    onClick={() => setActiveTab('readme')}
                    className={activeTab === 'readme' ? 'doc-tab active' : 'doc-tab'}
                    style={{
                      background: 'none',
                      border: 'none',
                      padding: '10px 14px',
                      cursor: 'pointer',
                      fontSize: '0.9rem',
                      color: activeTab === 'readme' ? 'var(--accent)' : 'var(--text-secondary)',
                      borderBottom: activeTab === 'readme' ? '2px solid var(--accent)' : '2px solid transparent',
                      marginBottom: '-1px',
                    }}
                  >
                    {t('detail.tab_readme')}
                  </button>
                )}
                {skillMd && (
                  <button
                    type="button"
                    onClick={() => setActiveTab('skill')}
                    className={activeTab === 'skill' ? 'doc-tab active' : 'doc-tab'}
                    style={{
                      background: 'none',
                      border: 'none',
                      padding: '10px 14px',
                      cursor: 'pointer',
                      fontSize: '0.9rem',
                      color: activeTab === 'skill' ? 'var(--accent)' : 'var(--text-secondary)',
                      borderBottom: activeTab === 'skill' ? '2px solid var(--accent)' : '2px solid transparent',
                      marginBottom: '-1px',
                    }}
                  >
                    {t('detail.tab_skill_md')}
                  </button>
                )}
              </div>
            )}
            {(() => {
              const body = activeTab === 'readme' ? readmeMd : skillMd;
              if (!body) {
                return (
                  <div style={{ color: 'var(--text-muted)', padding: '40px 0', textAlign: 'center' }}>
                    {t('detail.no_content')}
                  </div>
                );
              }
              return (
                <div className="markdown-body" dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(marked.parse(body) as string) }} />
              );
            })()}
          </div>

          <div>
            <div className="detail-sidebar">
              {latestVersion && (
                <>
                  <div className="sidebar-section">
                    <div className="sidebar-label">{t('detail.install')}</div>
                    <CodeBlock command={`skillhub install @${skill.namespaceSlug || skill.ownerHandle}/${skill.slug}`} style={{ padding: '10px 14px', fontSize: '0.8rem' }} />
                  </div>
                  <div className="sidebar-section">
                    <div className="sidebar-label">{t('detail.latest_version')}</div>
                    <div className="sidebar-value" style={{ fontFamily: 'monospace', color: 'var(--accent)' }}>v{latestVersion.version}</div>
                  </div>
                </>
              )}
              <div className="sidebar-section">
                <div className="sidebar-label">{t('detail.published_by')}</div>
                <div className="owner-info">
                  <div className="owner-avatar">{initial}</div>
                  <span className="sidebar-value">{skill.ownerHandle}</span>
                </div>
              </div>
              <div className="sidebar-section">
                <div className="sidebar-label">{t('detail.created')}</div>
                <div className="sidebar-value">{fmt(skill.createdAt)}</div>
              </div>
              <div className="sidebar-section">
                <div className="sidebar-label">{t('detail.updated')}</div>
                <div className="sidebar-value">{fmt(skill.updatedAt)}</div>
              </div>
              {latestVersion && (
                <div className="sidebar-section">
                  <a href={skillDownloadURL(skill.slug, latestVersion.version, skill.namespaceSlug || '')} className="btn btn-secondary" style={{ width: '100%', justifyContent: 'center' }}>
                    &#8615; {t('detail.download_zip')}
                  </a>
                </div>
              )}
            </div>

            {versions.length > 0 && (
              <div className="detail-sidebar" style={{ marginTop: 16 }}>
                <div className="sidebar-label" style={{ marginBottom: 12 }}>{t('detail.version_history')}</div>
                <ul className="version-list">
                  {versions.map(v => (
                    <li className="version-item" key={v.version}>
                      <span className="version-tag">v{v.version}</span>
                      <span className="version-date">{fmt(v.createdAt)}</span>
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
