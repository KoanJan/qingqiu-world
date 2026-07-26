import { useState, useEffect } from 'react';
import { Tooltip, Spin, message, Button } from 'antd';
import { useTranslation } from 'react-i18next';
import useScrolling from './hooks/useScrolling';
import useAppearance, { getBackgroundUrl } from './hooks/useAppearance';
import SessionList from './components/SessionList';
import ChatWindow from './components/ChatWindow';
import LLMConfigList from './components/LLMConfigList';
import EmbeddingConfigForm from './components/EmbeddingConfigForm';
import AgentConfig from './components/AgentConfig';
import SearchConfigForm from './components/SearchConfigForm';
import UserProfileForm from './components/UserProfileForm';
import AppearancePanel from './components/AppearancePanel';
import ResizableCard from './components/ResizableCard';
import PanelDetail from './components/PanelDetail';
import NappingCatButton from './components/NappingCatButton';
import KnowledgeBaseList from './components/KnowledgeBaseList';
import KnowledgeBaseDetail from './components/KnowledgeBaseDetail';
import SystemLLMConfigForm from './components/SystemLLMConfigForm';
import PublicExperienceList from './components/PublicExperienceList';
import PublicExperienceDetail from './components/PublicExperienceDetail';
import ConfigIcon from './components/ConfigIcon';
import { versionApi, userProfileApi, embeddingConfigApi, systemLLMConfigApi, initApiClient } from './services/api';
import { logger } from './logger';
import type { IconType } from './components/ConfigIcon';
import type { Session, LLMConfig, KnowledgeBase, PublicExperience } from './types';
import { TEMP_SESSION_ID } from './types';
import './App.css';

// Big view ring: each click of the switch button advances to the next view.
// To add a new big view, append its identifier to this array.
const RING = ['chat', 'settings'] as const;

// Settings sub-view identifiers (navigation within the settings big view).
// 'overview' is gone — the two-pane layout keeps a persistent left nav, so
// there is no separate overview page anymore. 'kb-detail' is a nested view
// reached from 'kb' (still uses PanelDetail's back button to return to kb).
type SettingsSubview =
  | 'user'
  | 'agent'
  | 'library'
  | 'kb-detail'
  | 'exp-detail'
  | 'llm'
  | 'embedding'
  | 'search'
  | 'custom';

// Sidebar navigation items. KB and public-experience are merged into a single
// 'library' entry; the right panel switches between them via a local tab bar.
const SETTINGS_CARDS: { key: SettingsSubview; iconType: IconType }[] = [
  { key: 'agent', iconType: 'agent' },
  { key: 'library', iconType: 'library' },
  { key: 'llm', iconType: 'llm' },
  { key: 'embedding', iconType: 'embedding' },
  { key: 'search', iconType: 'search' },
  { key: 'custom', iconType: 'custom' },
  { key: 'user', iconType: 'user' },
];

function App() {
  const { t } = useTranslation();
  const [currentSession, setCurrentSession] = useState<Session | null>(null);
  // Big view ring index. Starts at 0 (chat).
  const [viewIndex, setViewIndex] = useState(0);
  // Whether a slide animation is in progress. Prevents overlapping clicks.
  const [sliding, setSliding] = useState(false);
  // Temporarily disables the CSS transition so we can instantly re-order
  // panels after a slide completes (resetting for the next one-directional slide).
  const [noTransition, setNoTransition] = useState(false);
  // Visual order of panels in the track. The current panel is always on the
  // left (order 1) so that translateX(-100%) slides it out to the left
  // and reveals the next panel from the right.
  const [panelOrder, setPanelOrder] = useState<{ chat: number; settings: number }>({ chat: 1, settings: 2 });
  // Current subview within the settings big view. Defaults to 'user' — always
  // available (no embedding dependency), a neutral entry point.
  const [settingsSubview, setSettingsSubview] = useState<SettingsSubview>('user');
  const [refreshKey, setRefreshKey] = useState(0);
  const [systemLLMRefreshKey, setSystemLLMRefreshKey] = useState(0);
  const [showCreateAgent, setShowCreateAgent] = useState(false);
  const [showCreateLLM, setShowCreateLLM] = useState(false);
  const [showCreateKB, setShowCreateKB] = useState(false);
  const [showIngestExp, setShowIngestExp] = useState(false);
  // Library sub-tab: 'kb' or 'public-experience'. Resets to 'kb' on entry so
  // the user always lands on the KB list first.
  const [libraryTab, setLibraryTab] = useState<'kb' | 'public-experience'>('kb');
  const [selectedKB, setSelectedKB] = useState<KnowledgeBase | null>(null);
  const [selectedExp, setSelectedExp] = useState<PublicExperience | null>(null);
  const [version, setVersion] = useState<string>('');
  const [isMacElectron, setIsMacElectron] = useState(false);
  const [isWinLinuxElectron, setIsWinLinuxElectron] = useState(false);
  const [userProfileReady, setUserProfileReady] = useState(false);
  const [userProfileChecking, setUserProfileChecking] = useState(true);
  const [embeddingReady, setEmbeddingReady] = useState(false);
  const [systemLLMReady, setSystemLLMReady] = useState(false);

  useScrolling();

  const {
    settings: appearance,
    loading: appearanceLoading,
    selectColor,
    addCustomColor,
    removeCustomColor,
    selectBgImage,
    uploadBgImage,
    deleteUserImage,
    fetchUserImages,
    updateGlassOpacity,
    updateGlassBlur,
    updateLanguage,
    resetBackground,
    resetOverlay,
  } = useAppearance();

  // Sync appearance CSS custom properties to :root so they cascade to all elements.
  useEffect(() => {
    document.documentElement.style.setProperty('--glass-opacity', String(appearance.glassOpacity));
    document.documentElement.style.setProperty('--glass-blur', `${appearance.glassBlur}px`);
  }, [appearance.glassOpacity, appearance.glassBlur]);

  useEffect(() => {
    if (window.electronAPI) {
      window.electronAPI.getPlatform().then(platform => {
        setIsMacElectron(platform === 'darwin');
        setIsWinLinuxElectron(platform !== 'darwin');
      });
      initApiClient();
    }
  }, []);

  useEffect(() => {
    if (!window.electronAPI?.onBackendStatus) return;
    const unsubscribe = window.electronAPI.onBackendStatus((status) => {
      if (status === 'ready') {
        setRefreshKey(prev => prev + 1);
        versionApi.get()
          .then(res => setVersion(res.data.version))
          .catch(() => setVersion(''));
      }
    });
    return unsubscribe;
  }, []);

  // On mount, check if user profile exists.
  useEffect(() => {
    setUserProfileChecking(true);
    userProfileApi.get()
      .then((res) => {
        if (res.data.id) {
          setUserProfileReady(true);
        } else {
          setUserProfileReady(false);
        }
        setUserProfileChecking(false);
      })
      .catch(() => {
        setUserProfileReady(false);
        setUserProfileChecking(false);
      });
  }, []);

  // After user profile is confirmed, check if embedding is configured.
  useEffect(() => {
    if (!userProfileReady) return;
    embeddingConfigApi.get()
      .then((res) => {
        setEmbeddingReady(!!res.data.id);
      })
      .catch(() => {
        setEmbeddingReady(false);
      });
  }, [userProfileReady]);

  // After user profile is confirmed, check if the system LLM is configured.
  useEffect(() => {
    if (!userProfileReady) return;
    systemLLMConfigApi.get()
      .then((res) => {
        setSystemLLMReady(!!res.data.llm_config_id);
      })
      .catch(() => {
        setSystemLLMReady(false);
      });
  }, [userProfileReady]);

  useEffect(() => {
    if (!window.electronAPI?.onBackendError) return;
    const unsubscribe = window.electronAPI.onBackendError((error) => {
      message.error(`Backend failed to start: ${error}`, 0);
    });
    return unsubscribe;
  }, []);

  useEffect(() => {
    versionApi.get()
      .then(res => setVersion(res.data.version))
      .catch(() => setVersion(''));
  }, []);

  const handleSelectSession = (session: Session | null) => {
    setCurrentSession(session);
  };

  const handleSelectLLMConfig = (config: LLMConfig | null) => {
    if (currentSession && config) {
      setCurrentSession(prev => prev ? {
        ...prev,
        llm_config_id: config.id
      } : null);
    }
  };

  const handleCreateSession = (agentId: number) => {
    logger.info('handleCreateSession called with agentId:', agentId);
    const tempSession: Session = {
      id: TEMP_SESSION_ID,
      title: 'New Chat',
      agent_id: agentId,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    setCurrentSession(tempSession);
  };

  const handleAgentCreated = () => {
    setRefreshKey(prev => prev + 1);
  };

  const settingsLabelMap: Record<SettingsSubview, string> = {
    user: t('settings.userProfile'),
    agent: t('settings.agentConfig'),
    library: t('library.title'),
    'kb-detail': '',
    'exp-detail': '',
    llm: t('settings.llmConfig'),
    embedding: t('settings.embeddingConfig'),
    search: t('settings.searchConfig'),
    custom: t('settings.custom'),
  };

  // Advance to the next big view in the ring. Always slides left.
  const handleSwitchBigView = () => {
    if (sliding) return;
    setSliding(true);
    setViewIndex(prev => (prev + 1) % RING.length);
  };

  // After the slide finishes, silently reset: move the now-current panel to
  // the left (order 1) and snap translateX back to 0 — all without animation.
  // The user sees no change (current panel stays in place); the track is just
  // prepared for the next one-directional slide.
  const handleTrackTransitionEnd = (e: React.TransitionEvent) => {
    if (e.propertyName !== 'transform') return;
    if (!sliding) return;
    setSliding(false);
    setNoTransition(true);
    setPanelOrder({
      chat: viewIndex === 0 ? 1 : 2,
      settings: viewIndex === 1 ? 1 : 2,
    });
    // Use double rAF to ensure the noTransition frame is painted before
    // re-enabling transitions.
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        setNoTransition(false);
      });
    });
  };

  const renderPersonalizationPanel = () => (
    <PanelDetail title={t('settings.custom')}>
      <AppearancePanel
        language={appearance.language}
        bgMode={appearance.bgMode}
        bgColor={appearance.bgColor}
        bgImage={appearance.bgImage}
        bgImageSource={appearance.bgImageSource}
        customColors={appearance.customColors}
        glassOpacity={appearance.glassOpacity}
        glassBlur={appearance.glassBlur}
        onLanguageChange={updateLanguage}
        onSelectColor={selectColor}
        onAddCustomColor={addCustomColor}
        onRemoveCustomColor={removeCustomColor}
        onSelectBgImage={selectBgImage}
        onUploadBgImage={uploadBgImage}
        onDeleteUserImage={deleteUserImage}
        fetchUserImages={fetchUserImages}
        onGlassOpacityChange={updateGlassOpacity}
        onGlassBlurChange={updateGlassBlur}
        onResetBackground={resetBackground}
        onResetOverlay={resetOverlay}
      />
    </PanelDetail>
  );

  const renderUserPanel = () => (
    <PanelDetail title={t('settings.userProfile')}>
      <UserProfileForm />
    </PanelDetail>
  );

  const renderAgentPanel = () => (
    <PanelDetail
      title={t('settings.agentConfig')}
      onAdd={() => setShowCreateAgent(true)}
    >
      <AgentConfig
        showCreate={showCreateAgent}
        onCreateClose={() => setShowCreateAgent(false)}
        onAgentCreated={handleAgentCreated}
      />
    </PanelDetail>
  );

  const renderLibraryPanel = () => (
    <PanelDetail title={t('library.title')}>
      <div className="library-tab-cards">
        <button
          type="button"
          className={`library-tab-card${libraryTab === 'kb' ? ' active' : ''}`}
          onClick={() => setLibraryTab('kb')}
        >
          <ConfigIcon type="kb" size={30} iconSize={13} borderRadius="6px" marginBottom={0} />
          <span>{t('settings.kbConfig')}</span>
        </button>
        <button
          type="button"
          className={`library-tab-card${libraryTab === 'public-experience' ? ' active' : ''}`}
          onClick={() => setLibraryTab('public-experience')}
        >
          <ConfigIcon type="exp" size={30} iconSize={13} borderRadius="6px" marginBottom={0} />
          <span>{t('settings.publicExperience')}</span>
        </button>
      </div>
      <div style={{ marginBottom: 12 }}>
        {libraryTab === 'kb' ? (
          <Button type="primary" onClick={() => setShowCreateKB(true)}>
            {t('kb.create')}
          </Button>
        ) : (
          <Tooltip title={!systemLLMReady ? t('systemLLMRequired.message_1') : undefined}>
            <Button
              type="primary"
              disabled={!systemLLMReady}
              onClick={() => setShowIngestExp(true)}
            >
              {t('publicExperience.ingest')}
            </Button>
          </Tooltip>
        )}
      </div>
      <div style={{ marginTop: 16 }}>
        {libraryTab === 'kb' ? (
          <KnowledgeBaseList
            showCreate={showCreateKB}
            onCreateClose={() => setShowCreateKB(false)}
            onSelectKB={(kb) => {
              setSelectedKB(kb);
              setSettingsSubview('kb-detail');
            }}
          />
        ) : (
          <PublicExperienceList
            showIngest={showIngestExp}
            onIngestClose={() => setShowIngestExp(false)}
            onSelectExp={(exp) => {
              setSelectedExp(exp);
              setSettingsSubview('exp-detail');
            }}
          />
        )}
      </div>
    </PanelDetail>
  );

  const renderKBDetailPanel = () =>
    selectedKB ? (
      <PanelDetail
        onBack={() => {
          setSelectedKB(null);
          setShowCreateKB(false);
          setSettingsSubview('library');
        }}
      >
        <KnowledgeBaseDetail kb={selectedKB} />
      </PanelDetail>
    ) : null;

  const renderExpDetailPanel = () =>
    selectedExp ? (
      <PanelDetail
        onBack={() => {
          setSelectedExp(null);
          setSettingsSubview('library');
        }}
      >
        <PublicExperienceDetail
          exp={selectedExp}
          onRedistilled={() => {
            setSelectedExp(null);
            setSettingsSubview('library');
          }}
        />
      </PanelDetail>
    ) : null;

  const renderLLMPanel = () => (
    <PanelDetail
      title={t('settings.llmConfig')}
      onAdd={() => setShowCreateLLM(true)}
    >
      <LLMConfigList
        onSelectConfig={handleSelectLLMConfig}
        showCreate={showCreateLLM}
        onCreateClose={() => setShowCreateLLM(false)}
        onConfigChanged={() => setSystemLLMRefreshKey(k => k + 1)}
        beforeDelete={async (id) => {
          try {
            const sysRes = await systemLLMConfigApi.get();
            if (sysRes.data?.llm_config_id === id) {
              message.error(t('llmConfig.inUseError'));
              return false;
            }
          } catch { /* proceed with deletion if we can't check */ }
          return true;
        }}
      />
      <div style={{ borderTop: '1px solid var(--color-border)', marginTop: 32, paddingTop: 24 }}>
        <h4 style={{ marginBottom: 16 }}>{t('systemLLMConfig.title')}</h4>
        <SystemLLMConfigForm onSaved={() => setSystemLLMReady(true)} refreshKey={systemLLMRefreshKey} />
      </div>
    </PanelDetail>
  );

  const renderEmbeddingPanel = () => (
    <PanelDetail title={t('settings.embeddingConfig')}>
      <EmbeddingConfigForm onCreated={() => setEmbeddingReady(true)} />
    </PanelDetail>
  );

  const renderSearchPanel = () => (
    <PanelDetail title={t('settings.searchConfig')}>
      <SearchConfigForm />
    </PanelDetail>
  );

  const settingsPanelMap: Record<string, () => React.ReactNode> = {
    user: renderUserPanel,
    agent: renderAgentPanel,
    library: renderLibraryPanel,
    'kb-detail': renderKBDetailPanel,
    'exp-detail': renderExpDetailPanel,
    llm: renderLLMPanel,
    embedding: renderEmbeddingPanel,
    search: renderSearchPanel,
    custom: renderPersonalizationPanel,
  };

  const renderSettingsPanel = () => {
    const renderer = settingsPanelMap[settingsSubview];
    return renderer ? renderer() : null;
  };

  // Wait for appearance settings to load from the backend before rendering
  // anything — this ensures language, background, etc. are correct on first paint.
  if (appearanceLoading) {
    return (
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        height: '100vh', width: '100vw', background: '#ffffff',
      }}>
        <Spin size="large" />
      </div>
    );
  }

  // If user profile is still being checked, show full-screen loading.
  if (userProfileChecking) {
    return (
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        height: '100vh', width: '100vw', background: 'var(--color-bg)',
      }}>
        <Spin size="large" />
      </div>
    );
  }

  // If user has not set up their profile, show full-screen onboarding page.
  if (!userProfileReady) {
    return (
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        height: '100vh', width: '100vw', background: 'var(--color-bg)',
      }}>
        <div style={{ textAlign: 'center', maxWidth: 400, padding: '40px 32px' }}>
          <UserProfileForm onCreated={() => setUserProfileReady(true)} welcome />
        </div>
      </div>
    );
  }

  return (
    <div
      className="app-container"
      style={{
        ...(appearance.bgMode === 'color'
          ? { background: appearance.bgColor }
          : appearance.bgImage
            ? { backgroundImage: `url(${getBackgroundUrl(appearance.bgImage, appearance.bgImageSource)})` }
            : {}),
      } as any}
    >
      <header className={`app-header${isMacElectron ? ' app-header-mac' : ''}${isWinLinuxElectron ? ' app-header-win-linux' : ''}`}>
        <Tooltip title={version ? `v${version}` : ''} placement="right">
          <div className="app-logo">
            <img src="./favicon.png" alt="logo" className="app-logo-img" />
            Qingqiu World
          </div>
        </Tooltip>
        <div className="app-header-actions">
          <NappingCatButton onClick={handleSwitchBigView} disabled={sliding} />
        </div>
      </header>

      <div className="app-body">
        {/* Viewport: clips the track so only one big view is visible. */}
        <div className="app-bigview-viewport">
          {/* Track: holds all big views side by side. Slides left on each
              switch. After the slide, order is silently reset so the next
              switch also slides left (one-directional). */}
          <div
            className="app-bigview-track"
            style={{
              transform: sliding ? 'translateX(-100%)' : 'translateX(0)',
              transition: noTransition ? 'none' : undefined,
            }}
            onTransitionEnd={handleTrackTransitionEnd}
          >
            {/* Chat big view: session list + chat window. */}
            <div className="app-bigview-chat" style={{ order: panelOrder.chat }}>
              <ResizableCard
                defaultWidth={280}
                minWidth={200}
                maxWidth={400}
                resizeSide="right"
                className="app-sidebar-wrapper"
              >
                <SessionList
                  key={refreshKey}
                  currentSessionId={currentSession?.id || null}
                  embeddingReady={embeddingReady}
                  onSelectSession={handleSelectSession}
                  onCreateSession={handleCreateSession}
                />
              </ResizableCard>

              <div className="app-content">
                <ResizableCard flex className="app-chat-area-wrapper">
                  <ChatWindow
                    session={currentSession}
                    onSessionCreated={(sessionId) => {
                      setRefreshKey(prev => prev + 1);
                      setCurrentSession(prev => prev ? { ...prev, id: sessionId } : null);
                    }}
                  />
                </ResizableCard>
              </div>
            </div>

            {/* Settings big view: two-pane (persistent left nav + right
                detail), each wrapped in a ResizableCard so they read as
                distinct floating cards — mirroring the chat big view's
                sidebar/content card treatment. */}
            <div className="app-bigview-settings" style={{ order: panelOrder.settings }}>
              <ResizableCard
                defaultWidth={220}
                minWidth={180}
                maxWidth={320}
                resizeSide="right"
                className="settings-sidebar-wrapper"
              >
                <div className="settings-sidebar">
                  <div className="settings-sidebar-title">{t('settings.title')}</div>
                  <nav className="settings-nav">
                    {SETTINGS_CARDS.map(({ key, iconType }) => {
                      const needsEmbedding = key === 'agent' || key === 'library';
                      const disabled = needsEmbedding && !embeddingReady;
                      // kb-detail is nested under kb; keep kb highlighted while
                      // the detail page is open so the nav reflects the location.
                      const activeKey = settingsSubview === 'kb-detail' || settingsSubview === 'exp-detail' ? 'library' : settingsSubview;
                      return (
                        <Tooltip key={key} title={disabled ? t('embeddingRequired.message_1') : undefined}>
                          <button
                            type="button"
                            className={`settings-nav-item${activeKey === key ? ' active' : ''}`}
                            disabled={disabled}
                            onClick={() => setSettingsSubview(key)}
                          >
                            <ConfigIcon type={iconType} size={28} iconSize={14} borderRadius="6px" marginBottom={0} />
                            <span className="settings-nav-label">{settingsLabelMap[key]}</span>
                          </button>
                        </Tooltip>
                      );
                    })}
                  </nav>
                </div>
              </ResizableCard>

              <div className="app-content">
                <ResizableCard flex className="settings-content-wrapper">
                  {renderSettingsPanel()}
                </ResizableCard>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

export default App;
