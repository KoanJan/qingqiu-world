import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Button, Checkbox, Divider, Form, Input, Select, Spin, Tabs, Upload, message } from 'antd';
import { SaveOutlined, UploadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { Agent, AgentVoice, LLMConfig, TTSProviderDefinition, TTSRenderer } from '../types';
import { API_BUSINESS_CODE, agentApi, agentVoiceApi, getAvatarUrl, llmConfigApi, ttsRendererApi, uploadApi } from '../services/api';
import { VOICE_LOCALE_OPTIONS } from '../constants/voice';
import { logger } from '../logger';

interface AgentDetailProps {
  agent: Agent;
  onUpdated: (agent: Agent) => void;
}

// AgentDetail is the dedicated configuration page for one Agent. Voice identity
// lives here because it is an Agent property, while renderers remain external
// provider connections selected explicitly by the user.
const AgentDetail: React.FC<AgentDetailProps> = ({ agent, onUpdated }) => {
  const { t, i18n } = useTranslation();
  const [basicForm] = Form.useForm();
  const [voiceForm] = Form.useForm();
  const [llmConfigs, setLLMConfigs] = useState<LLMConfig[]>([]);
  const [providers, setProviders] = useState<TTSProviderDefinition[]>([]);
  const [renderers, setRenderers] = useState<TTSRenderer[]>([]);
  const [voice, setVoice] = useState<AgentVoice | null>(null);
  const [loadingVoice, setLoadingVoice] = useState(true);
  const [voiceLoadFailed, setVoiceLoadFailed] = useState(false);
  const [savingBasic, setSavingBasic] = useState(false);
  const [savingVoice, setSavingVoice] = useState(false);
  const [basicDirty, setBasicDirty] = useState(false);
  const [voiceDirty, setVoiceDirty] = useState(false);
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const [avatarPreview, setAvatarPreview] = useState('');
  const [sampleAudio, setSampleAudio] = useState<File | null>(null);
  const detailRequestIDRef = useRef(0);
  const basicSaveRequestIDRef = useRef(0);
  const voiceSaveRequestIDRef = useRef(0);
  const selectedRendererID = Form.useWatch('tts_renderer_id', voiceForm);
  const selectedRenderer = useMemo(
    () => renderers.find(renderer => renderer.id === selectedRendererID),
    [renderers, selectedRendererID],
  );
  const selectedProvider = useMemo(
    () => providers.find(provider => provider.provider === selectedRenderer?.provider),
    [providers, selectedRenderer],
  );
  // The renderer binding is one value where 0 means "no TTS renderer". It is
  // compared with the persisted binding, so an untouched form is never dirty.
  const selectedRendererValue = selectedRendererID === undefined || selectedRendererID === null ? 0 : Number(selectedRendererID);
  const boundRendererID = voice?.tts_renderer_id ?? 0;
  const rendererBindingChanged = selectedRendererValue !== boundRendererID;
  // Terms are requested only when a confirmed provider connection becomes newly bound.
  const showTerms = rendererBindingChanged && selectedRendererValue > 0 && Boolean(selectedProvider?.terms_url);
  const hasEffectiveSample = sampleAudio !== null || Boolean(voice?.sample_audio_filename);

  useEffect(() => () => {
    if (avatarPreview) URL.revokeObjectURL(avatarPreview);
  }, [avatarPreview]);

  // A voice save is actionable when a new sample is picked or the renderer
  // binding changes. The two resources are independent, so either alone suffices.
  const isVoiceDirty = (values: Record<string, unknown>, audio: File | null) => {
    if (audio !== null) return true;
    return Number(values.tts_renderer_id ?? 0) !== (voice?.tts_renderer_id ?? 0)
      || String(values.sample_transcript ?? '') !== (voice?.sample_transcript ?? '')
      || String(values.sample_locale ?? '') !== (voice?.sample_locale ?? '');
  };

  const loadDetail = async () => {
    const requestID = ++detailRequestIDRef.current;
    // A save already sent for the previous Agent may still finish, but its
    // response must not mutate the newly selected Agent's form state.
    basicSaveRequestIDRef.current += 1;
    voiceSaveRequestIDRef.current += 1;
    setLoadingVoice(true);
    setSavingBasic(false);
    setSavingVoice(false);
    basicForm.setFieldsValue({
      character_settings: agent.character_settings,
      llm_config_id: agent.llm_config_id,
    });
    setAvatarFile(null);
    setAvatarPreview('');
    setSampleAudio(null);
    setBasicDirty(false);
    setVoiceDirty(false);
    setVoiceLoadFailed(false);
    voiceForm.resetFields();
    try {
      const [llmResponse, providerResponse, rendererResponse] = await Promise.all([
        llmConfigApi.list(),
        ttsRendererApi.listProviders(),
        ttsRendererApi.list(),
      ]);
      if (requestID !== detailRequestIDRef.current) return;
      setLLMConfigs(llmResponse.data);
      setProviders(providerResponse.data);
      setRenderers(rendererResponse.data);
      try {
        const voiceResponse = await agentVoiceApi.get(agent.id);
        if (requestID !== detailRequestIDRef.current) return;
        setVoice(voiceResponse.data);
        voiceForm.setFieldsValue({
          tts_renderer_id: voiceResponse.data.tts_renderer_id,
          sample_locale: voiceResponse.data.sample_locale,
          sample_transcript: voiceResponse.data.sample_transcript,
        });
      } catch (error) {
        if (requestID !== detailRequestIDRef.current) return;
        const code = (error as { code?: number }).code;
        if (code !== API_BUSINESS_CODE.NOT_FOUND) {
          logger.error('Failed to load Agent voice identity', error, 'agent_id', agent.id);
          setVoice(null);
          setVoiceLoadFailed(true);
          message.error(t('ttsConfig.loadFailed'));
          return;
        }
        setVoice(null);
      }
    } catch (error) {
      if (requestID !== detailRequestIDRef.current) return;
      logger.error('Failed to load Agent configuration', error, 'agent_id', agent.id);
      setVoice(null);
      setVoiceLoadFailed(true);
      message.error(t('messages.loadFailed'));
    } finally {
      if (requestID === detailRequestIDRef.current) setLoadingVoice(false);
    }
  };

  useEffect(() => {
    void loadDetail();
    return () => {
      detailRequestIDRef.current += 1;
      basicSaveRequestIDRef.current += 1;
      voiceSaveRequestIDRef.current += 1;
    };
    // Agent identity is the reload boundary. Language changes must not discard
    // unsaved voice or basic-form edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id]);

  const rendererName = (rendererID: number) => {
    const renderer = renderers.find(item => item.id === rendererID);
    return renderer?.name ?? t('ttsConfig.unknownProvider');
  };

  const saveBasic = async (values: Record<string, unknown>) => {
    const requestID = ++basicSaveRequestIDRef.current;
    setSavingBasic(true);
    try {
      let avatar = agent.avatar;
      if (avatarFile) avatar = (await uploadApi.uploadAvatar(avatarFile)).data.filename;
      const updated = (await agentApi.update(agent.id, { ...values, avatar })).data;
      if (requestID !== basicSaveRequestIDRef.current) return;
      onUpdated(updated);
      setAvatarFile(null);
      setBasicDirty(false);
      message.success(t('agent.updateSuccess'));
    } catch (error) {
      logger.error('Failed to update Agent', error, 'agent_id', agent.id);
      if (requestID === basicSaveRequestIDRef.current) {
        message.error(t('agent.updateFailed'));
      }
    } finally {
      if (requestID === basicSaveRequestIDRef.current) {
        setSavingBasic(false);
      }
    }
  };

  // Reflect a saved voice and reset the form to the persisted values.
  const applyVoice = (data: AgentVoice) => {
    setVoice(data);
    setSampleAudio(null);
    voiceForm.setFieldsValue({
      tts_renderer_id: data.tts_renderer_id,
      sample_locale: data.sample_locale,
      sample_transcript: data.sample_transcript,
      terms_confirmed: false,
    });
    setVoiceDirty(false);
  };

  // One declarative save sends every scalar field; file presence alone means
  // replacement. The backend decides whether a new immutable version is needed.
  const saveVoice = async (values: Record<string, unknown>) => {
    const requestID = ++voiceSaveRequestIDRef.current;
    const rendererID = Number(values.tts_renderer_id ?? 0);
    setSavingVoice(true);
    try {
      const response = await agentVoiceApi.save({
        personID: agent.id,
        ttsRendererID: rendererID,
        termsConfirmed: values.terms_confirmed === true,
        sampleLocale: String(values.sample_locale ?? ''),
        sampleTranscript: String(values.sample_transcript ?? ''),
        sampleAudio: sampleAudio ?? undefined,
      });
      if (requestID !== voiceSaveRequestIDRef.current) return;
      applyVoice(response.data);
      message.success(t('ttsConfig.voiceSaveSuccess'));
    } catch (error) {
      logger.error('Failed to save Agent voice', error, 'agent_id', agent.id);
      if (requestID === voiceSaveRequestIDRef.current) {
        message.error(t('ttsConfig.voiceSaveFailed'));
      }
    } finally {
      if (requestID === voiceSaveRequestIDRef.current) {
        setSavingVoice(false);
      }
    }
  };

  const renderVoiceForm = () => (
    <Form
      form={voiceForm}
      layout="vertical"
      onFinish={saveVoice}
      onValuesChange={(changedValues, values) => {
        if ('tts_renderer_id' in changedValues) voiceForm.setFieldValue('terms_confirmed', false);
        setVoiceDirty(isVoiceDirty(values, sampleAudio));
      }}
    >
      <section className="agent-voice-section">
        <div className="agent-voice-section-title">{t('ttsConfig.voiceSample')}</div>
        <Form.Item label={t('ttsConfig.sampleAudio')} extra={t('ttsConfig.voiceSampleHint')}>
          <Upload
            accept=".wav,.mp3,.flac,audio/wav,audio/mpeg,audio/flac"
            maxCount={1}
            beforeUpload={(file) => {
              setSampleAudio(file);
              if (!voiceForm.getFieldValue('sample_locale')) {
                voiceForm.setFieldValue('sample_locale', i18n.language === 'zh' ? 'zh-CN' : 'en-US');
              }
              setVoiceDirty(true);
              return false;
            }}
            onRemove={() => { setSampleAudio(null); setVoiceDirty(isVoiceDirty(voiceForm.getFieldsValue(), null)); return true; }}
            fileList={sampleAudio
              ? [{ uid: 'sample-audio', name: sampleAudio.name, status: 'done' }]
              : voice?.sample_audio_filename
                ? [{ uid: 'saved-sample-audio', name: voice.sample_audio_filename, status: 'done' }]
                : []}
            showUploadList={{ showRemoveIcon: sampleAudio !== null }}
          >
            <Button icon={<UploadOutlined />}>{t('ttsConfig.selectSampleAudio')}</Button>
          </Upload>
        </Form.Item>
        <Form.Item name="sample_transcript" label={t('ttsConfig.sampleTranscript')} rules={hasEffectiveSample ? [{ required: true, message: t('ttsConfig.sampleTranscriptRequired') }] : []}>
          <Input.TextArea rows={3} placeholder={t('ttsConfig.sampleTranscriptPlaceholder')} />
        </Form.Item>
        <Form.Item name="sample_locale" label={t('ttsConfig.sampleLocale')} rules={hasEffectiveSample ? [{ required: true }] : []}>
          <Select options={VOICE_LOCALE_OPTIONS.map(option => ({ value: option.value, label: i18n.language === 'zh' ? option.label.zh : option.label.en }))} />
        </Form.Item>
      </section>
      <Divider />
      <section className="agent-voice-section">
        <div className="agent-voice-section-title">{t('ttsConfig.ttsBinding')}</div>
        <Form.Item name="tts_renderer_id" label={t('ttsConfig.renderer')}>
          <Select
            placeholder={t('ttsConfig.rendererPlaceholder')}
            options={[{ value: 0, label: t('ttsConfig.noRenderer') }, ...renderers.map(renderer => ({
              value: renderer.id,
              label: rendererName(renderer.id),
            }))]}
          />
        </Form.Item>
        {showTerms && <>
          <p className="tts-terms-note">{t('ttsConfig.termsNotice')} <a href={selectedProvider?.terms_url} target="_blank" rel="noreferrer">{t('ttsConfig.readTerms')}</a></p>
          <Form.Item name="terms_confirmed" valuePropName="checked" rules={[{ required: true, message: t('ttsConfig.termsConfirmRequired') }]}>
            <Checkbox>{t('ttsConfig.termsConfirm')}</Checkbox>
          </Form.Item>
        </>}
      </section>
      <div className="agent-detail-save-action">
        <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={savingVoice} disabled={!voiceDirty}>{t('agent.saveVoiceConfig')}</Button>
      </div>
    </Form>
  );

  return (
    <div className="agent-detail">
      <div className="agent-detail-header">
        <div className="agent-detail-title">{agent.name}</div>
      </div>
      <Tabs
        className="agent-detail-tabs"
        items={[
          {
            key: 'basic', label: t('agent.basicConfig'), children: (
              <Form
                form={basicForm}
                layout="vertical"
                onFinish={saveBasic}
                className="agent-detail-form"
                onValuesChange={(_, values) => setBasicDirty(
                  avatarFile !== null
                  || String(values.character_settings ?? '') !== agent.character_settings
                  || Number(values.llm_config_id ?? 0) !== agent.llm_config_id,
                )}
              >
                <Form.Item label={t('agent.avatar')}>
                  <Upload showUploadList={false} accept="image/*" beforeUpload={(file) => {
                    if (avatarPreview) URL.revokeObjectURL(avatarPreview);
                    setAvatarFile(file);
                    setAvatarPreview(URL.createObjectURL(file));
                    setBasicDirty(true);
                    return false;
                  }}>
                    {avatarPreview || agent.avatar ? <img className="avatar-upload-preview" src={avatarPreview || getAvatarUrl(agent.avatar)} alt="" /> : <Button>{t('agent.avatarUpload')}</Button>}
                  </Upload>
                </Form.Item>
                <div className="agent-detail-name"><span>{t('agent.name')}</span><strong>{agent.name}</strong></div>
                <Form.Item name="character_settings" label={t('agent.characterSettings')}><Input.TextArea rows={6} placeholder={t('agent.characterSettingsPlaceholder')} /></Form.Item>
                <Form.Item name="llm_config_id" label={t('agent.llmConfigId')} rules={[{ required: true, message: t('agent.llmConfigIdPlaceholder') }]}>
                  <Select placeholder={t('agent.llmConfigIdPlaceholder')} options={llmConfigs.map(config => ({ value: config.id, label: config.name }))} />
                </Form.Item>
                <div className="agent-detail-save-action">
                  <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={savingBasic} disabled={!basicDirty}>{t('agent.saveBasic')}</Button>
                </div>
              </Form>
            ),
          },
          {
            key: 'voice', label: t('agent.voiceConfig'), children: loadingVoice ? <div className="empty-state-text"><Spin size="small" /> {t('sidebar.loading')}</div> : voiceLoadFailed ? (
              <div className="empty-state-text">{t('ttsConfig.loadFailed')}</div>
            ) : (
              <div className="agent-detail-form">
                <div className="agent-voice-status">{voice?.sample_audio_filename ? `${t('agent.voiceReady')} · ${voice.sample_locale} · ${voice.tts_renderer_id ? rendererName(voice.tts_renderer_id) : t('ttsConfig.noRenderer')}` : t('agent.voiceNotConfigured')}</div>
                {renderVoiceForm()}
              </div>
            ),
          },
        ]}
      />
    </div>
  );
};

export default AgentDetail;
