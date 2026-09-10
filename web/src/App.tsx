import { useState, useEffect, useCallback } from 'react';
import { Tooltip, Spin, message } from 'antd';
import { DownOutlined } from '@ant-design/icons';
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
import PublicExperienceList from './components/PublicExperienceList';
import PublicExperienceDetail from './components/PublicExperienceDetail';
import JinshuPanel from './components/JinshuPanel';
import ConfigIcon from './components/ConfigIcon';
import { versionApi, userProfileApi, embeddingConfigApi, systemLLMConfigApi, initApiClient, sessionApi } from './services/api';
import { logger } from './logger';
import type { IconType } from './components/ConfigIcon';
import type { Session, LLMConfig, KnowledgeBase, PublicExperience } from './types';
import { useUserSSE, type UserNotification } from './hooks/useUserSSE';
import { TEMP_SESSION_ID } from './types';
import './App.css';

// Big view ring: each click of the switch button advances to the next view.
// To add a new big view, append its identifier to this array.
const RING = ['chat', 'mine', 'settings'] as const;

// Identifier of a big view in the ring.
type RingKey = typeof RING[number];

// Settings sub-view identifiers (navigation within the settings big view).
// 'overview' is gone — the two-pane layout keeps a persistent left nav, so
// there is no separate overview page anymore. KB and public experience are
// flat top-level entries (no wrapping 'library' grouping). 'kb-detail' /
// 'exp-detail' are nested views reached from 'kb' / 'experience' (still use
// PanelDetail's back button to return to their lists).
// The user profile entry lives in the mine big view, not here.
type SettingsSubview =
  | 'agent'
  | 'kb'
  | 'experience'
  | 'kb-detail'
  | 'exp-detail'
  | 'llm'
  | 'embedding'
  | 'search'
  | 'custom';

// Sidebar navigation items.
const SETTINGS_CARDS: { key: SettingsSubview; iconType: IconType }[] = [
  { key: 'agent', iconType: 'agent' },
  { key: 'kb', iconType: 'kb' },
  { key: 'experience', iconType: 'exp' },
  { key: 'llm', iconType: 'llm' },
  { key: 'embedding', iconType: 'embedding' },
  { key: 'search', iconType: 'search' },
  { key: 'custom', iconType: 'custom' },
];

// Mine sub-view identifiers (navigation within the mine big view).
// Jinshu (锦书) has two children: received and sent; the user profile is a
// top-level entry alongside the jinshu group.
type MineSubview = 'jinshu-received' | 'jinshu-sent' | 'user';

// Jinshu's child navigation items shown under the collapsible "jinshu" parent.
const MINE_CHILDREN: MineSubview[] = ['jinshu-received', 'jinshu-sent'];

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
  const [panelOrder, setPanelOrder] = useState<Record<RingKey, number>>({
    chat: 1,
    mine: 2,
    settings: 3,
  });
  // Current subview within the settings big view. Defaults to 'agent' — always
  // available (no embedding dependency), a neutral entry point.
  const [settingsSubview, setSettingsSubview] = useState<SettingsSubview>('agent');
  // Current subview within the mine big view.
  const [mineSubview, setMineSubview] = useState<MineSubview>('jinshu-received');
  // Whether the jinshu parent nav group is expanded.
  const [mineNavOpen, setMineNavOpen] = useState(true);
  const [refreshKey, setRefreshKey] = useState(0);
  const [showCreateAgent, setShowCreateAgent] = useState(false);
  const [showCreateLLM, setShowCreateLLM] = useState(false);
  const [showCreateKB, setShowCreateKB] = useState(false);
  const [showIngestExp, setShowIngestExp] = useState(false);
  const [selectedKB, setSelectedKB] = useState<KnowledgeBase | null>(null);
  const [selectedExp, setSelectedExp] = useState<PublicExperience | null>(null);
  const [version, setVersion] = useState<string>('');
  const [isMacElectron, setIsMacElectron] = useState(false);
  const [isWinLinuxElectron, setIsWinLinuxElectron] = useState(false);
  const [userProfileReady, setUserProfileReady] = useState(false);
  const [userProfileChecking, setUserProfileChecking] = useState(true);
  const [embeddingReady, setEmbeddingReady] = useState(false);
  const [systemLLMReady, setSystemLLMReady] = useState(false);
  const [notification, setNotification] = useState<{ sessionId: number; sequence: number } | null>(null);

  useScrolling();

  const handleUserNotification = useCallback((event: UserNotification) => {
    if (event.type !== 'new_message' || typeof event.session_id !== 'number') {
      return;
    }
    setNotification({ sessionId: event.session_id, sequence: Date.now() });
  }, []);

  useUserSSE({ enabled: userProfileReady, onNotification: handleUserNotification });

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

  // Re-check the system LLM singleton config; `systemLLMReady` gates features
  // that require a system LLM (e.g. experience ingestion).
  const refreshSystemLLMReady = async () => {
    try {
      const res = await systemLLMConfigApi.get();
      setSystemLLMReady(!!res.data?.llm_config_id);
    } catch {
      setSystemLLMReady(false);
    }
  };

  // After user profile is confirmed, check if the system LLM is configured.
  useEffect(() => {
    if (!userProfileReady) return;
    refreshSystemLLMReady();
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
    if (session && session.id !== TEMP_SESSION_ID) {
      sessionApi.markRead(session.id).catch(error => {
        logger.error('Failed to mark session read:', error, 'session_id', session.id);
      });
    }
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
      agent_name: '',
      agent_avatar: '',
      participants: [],
      is_participant: true,
      has_unread: false,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    setCurrentSession(tempSession);
  };

  const handleAgentCreated = () => {
    setRefreshKey(prev => prev + 1);
  };

  const settingsLabelMap: Record<SettingsSubview, string> = {
    agent: t('settings.agentConfig'),
    kb: t('settings.kbConfig'),
    experience: t('settings.publicExperience'),
    'kb-detail': '',
    'exp-detail': '',
    llm: t('settings.llmConfig'),
    embedding: t('settings.embeddingConfig'),
    search: t('settings.searchConfig'),
    custom: t('settings.custom'),
  };

  const mineLabelMap: Record<MineSubview, string> = {
    'jinshu-received': t('jinshu.received'),
    'jinshu-sent': t('jinshu.sent'),
    user: t('settings.userProfile'),
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
    // Put the now-current view at order 1 (left) and the next view at order 2
    // (right); the remaining view sits further right, off-screen. This resets
    // the track so the next switch is another leftward slide.
    const nextOrder = {} as Record<RingKey, number>;
    for (let i = 0; i < RING.length; i++) {
      nextOrder[RING[(viewIndex + i) % RING.length]] = i + 1;
    }
    setPanelOrder(nextOrder);
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

  const renderKBPanel = () => (
    <PanelDetail
      title={t('settings.kbConfig')}
      onAdd={() => setShowCreateKB(true)}
    >
      <KnowledgeBaseList
        showCreate={showCreateKB}
        onCreateClose={() => setShowCreateKB(false)}
        onSelectKB={(kb) => {
          setSelectedKB(kb);
          setSettingsSubview('kb-detail');
        }}
      />
    </PanelDetail>
  );

  const renderExpPanel = () => (
    <PanelDetail
      title={t('settings.publicExperience')}
      onAdd={() => setShowIngestExp(true)}
      onAddDisabled={!systemLLMReady}
      onAddTooltip={t('systemLLMRequired.message_1')}
    >
      <PublicExperienceList
        showIngest={showIngestExp}
        onIngestClose={() => setShowIngestExp(false)}
        onSelectExp={(exp) => {
          setSelectedExp(exp);
          setSettingsSubview('exp-detail');
        }}
      />
    </PanelDetail>
  );

  const renderKBDetailPanel = () =>
    selectedKB ? (
      <PanelDetail
        onBack={() => {
          setSelectedKB(null);
          setShowCreateKB(false);
          setSettingsSubview('kb');
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
          setSettingsSubview('experience');
        }}
      >
        <PublicExperienceDetail
          exp={selectedExp}
          onRedistilled={() => {
            setSelectedExp(null);
            setSettingsSubview('experience');
          }}
        />
      </PanelDetail>
    ) : null;

  const renderLLMPanel = () => (
    <PanelDetail
      title={t('settings.llmConfig')}
      onAdd={() => setShowCreateLLM(true)}
    >
      <p className="panel-note">{t('systemLLMConfig.description')}</p>
      <LLMConfigList
        onSelectConfig={handleSelectLLMConfig}
        showCreate={showCreateLLM}
        onCreateClose={() => setShowCreateLLM(false)}
        onConfigChanged={() => { refreshSystemLLMReady(); }}
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
    agent: renderAgentPanel,
    kb: renderKBPanel,
    experience: renderExpPanel,
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

  const renderMinePanel = () => {
    if (mineSubview === 'user') {
      return (
        <PanelDetail title={t('settings.userProfile')}>
          <UserProfileForm />
        </PanelDetail>
      );
    }
    if (mineSubview === 'jinshu-received') {
      return <JinshuPanel direction="received" />;
    }
    if (mineSubview === 'jinshu-sent') {
      return <JinshuPanel direction="sent" />;
    }
    return null;
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
                  notification={notification}
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

            {/* Mine big view: two-pane (left nav + right detail), mirroring
                the settings big view's sidebar/content treatment. */}
            <div className="app-bigview-mine" style={{ order: panelOrder.mine }}>
              <ResizableCard
                defaultWidth={220}
                minWidth={180}
                maxWidth={320}
                resizeSide="right"
                className="settings-sidebar-wrapper"
              >
                <div className="settings-sidebar">
                  <div className="settings-sidebar-title">{t('mine.title')}</div>
                  <nav className="settings-nav">
                    <div className="settings-nav-group">
                      <button
                        type="button"
                        className="settings-nav-item"
                        onClick={() => setMineNavOpen(prev => !prev)}
                      >
                        <ConfigIcon type="mail" size={28} iconSize={14} borderRadius="6px" marginBottom={0} />
                        <span className="settings-nav-label">{t('jinshu.title')}</span>
                        <DownOutlined className={`nav-chevron${mineNavOpen ? ' open' : ''}`} />
                      </button>
                      {mineNavOpen && (
                        <div className="settings-nav-children">
                          {MINE_CHILDREN.map((key) => (
                            <button
                              key={key}
                              type="button"
                              className={`settings-nav-item settings-nav-child${mineSubview === key ? ' active' : ''}`}
                              onClick={() => setMineSubview(key)}
                            >
                              <span className="settings-nav-label">{mineLabelMap[key]}</span>
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                    {/* Top-level entry: user profile (moved out of settings). */}
                    <button
                      type="button"
                      className={`settings-nav-item${mineSubview === 'user' ? ' active' : ''}`}
                      onClick={() => setMineSubview('user')}
                    >
                      <ConfigIcon type="user" size={28} iconSize={14} borderRadius="6px" marginBottom={0} />
                      <span className="settings-nav-label">{mineLabelMap.user}</span>
                    </button>
                  </nav>
                </div>
              </ResizableCard>

              <div className="app-content">
                <ResizableCard flex className="settings-content-wrapper">
                  {renderMinePanel()}
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
                      // KB creation needs embeddings; the experience list
                      // itself does not (ingest is gated by systemLLM on its
                      // own button).
                      const needsEmbedding = key === 'agent' || key === 'kb';
                      const disabled = needsEmbedding && !embeddingReady;
                      // Detail views are nested under their lists; keep the
                      // parent entry highlighted so the nav reflects the
                      // location.
                      const activeKey = settingsSubview === 'kb-detail' ? 'kb' : settingsSubview === 'exp-detail' ? 'experience' : settingsSubview;
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
