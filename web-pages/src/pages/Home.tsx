import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { listSkills, type Skill } from '@saker/skillhub-client/skills';
import SkillCard from '../components/SkillCard';
import CodeBlock from '../components/CodeBlock';

type InstallTab = 'agent' | 'human';

export default function Home() {
  const { t } = useTranslation();
  const [skills, setSkills] = useState<Skill[]>([]);
  const [installTab, setInstallTab] = useState<InstallTab>('agent');

  useEffect(() => {
    listSkills(12, '', 'downloads').then(r => setSkills(r.data || [])).catch(e => console.error('Failed to load skills:', e));
  }, []);

  return (
    <>
      <section className="hero-section">
        <div className="hero-glow" />
        <div className="container">
          <h1 className="hero-title">
            <span className="hero-line-1">{t('index.hero_title_1')}</span>
            <span className="hero-line-2">{t('index.hero_title_2')}</span>
          </h1>
          <p className="hero-subtitle">
            {t('index.hero_subtitle')}
          </p>
          <div className="hero-actions">
            <Link to="/skills" className="btn btn-primary">{t('index.browse')}</Link>
            <Link to="/publish" className="btn btn-secondary">{t('index.publish')}</Link>
          </div>
        </div>
      </section>

      <section style={{ padding: '0 0 48px' }}>
        <div className="container" style={{ maxWidth: 720 }}>
          <h2 style={{
            fontSize: '0.95rem',
            fontWeight: 600,
            marginBottom: 12,
            textAlign: 'center',
            opacity: 0.75,
          }}>
            {t('index.install_heading')}
          </h2>
          <div style={{
            display: 'flex',
            gap: 4,
            justifyContent: 'center',
            borderBottom: '1px solid var(--border)',
            marginBottom: 12,
          }}>
            {(['agent', 'human'] as InstallTab[]).map(tab => (
              <button
                key={tab}
                type="button"
                onClick={() => setInstallTab(tab)}
                className={installTab === tab ? 'doc-tab active' : 'doc-tab'}
                style={{
                  background: 'none',
                  border: 'none',
                  padding: '8px 14px',
                  cursor: 'pointer',
                  fontSize: '0.85rem',
                  color: installTab === tab ? 'var(--accent)' : 'var(--text-secondary)',
                  borderBottom: installTab === tab ? '2px solid var(--accent)' : '2px solid transparent',
                  marginBottom: '-1px',
                }}
              >
                {t(tab === 'agent' ? 'index.install_tab_agent' : 'index.install_tab_human')}
              </button>
            ))}
          </div>
          {installTab === 'agent' ? (
            <>
              <CodeBlock
                prefix=""
                command={t('index.install_prompt', {
                  url: `${window.location.origin}/skills.md`,
                })}
              />
              <p style={{
                fontSize: '0.85rem',
                marginTop: 10,
                textAlign: 'center',
                opacity: 0.6,
              }}>
                {t('index.install_hint')}
              </p>
            </>
          ) : (
            <>
              <CodeBlock
                prefix=""
                command={t('index.install_human_command')}
              />
              <p style={{
                fontSize: '0.85rem',
                marginTop: 10,
                textAlign: 'center',
                opacity: 0.6,
              }}>
                {t('index.install_hint_human')}
              </p>
            </>
          )}
        </div>
      </section>

      {skills.length > 0 && (
        <section style={{ padding: '0 0 64px' }}>
          <div className="container">
            <h2 style={{ fontSize: '1.5rem', fontWeight: 700, marginBottom: 24 }}>{t('index.popular')}</h2>
            <div className="skill-grid">
              {skills.map(s => (
                <SkillCard key={s.slug} {...s} />
              ))}
            </div>
            <div style={{ textAlign: 'center', marginTop: 32 }}>
              <Link to="/skills" className="btn btn-secondary">{t('index.view_all')} &rarr;</Link>
            </div>
          </div>
        </section>
      )}
    </>
  );
}
