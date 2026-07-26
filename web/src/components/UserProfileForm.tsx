import React, { useState, useEffect } from 'react';
import { Form, Input, Button, message, Spin } from 'antd';
import { SaveOutlined, ArrowRightOutlined, ArrowLeftOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { UserProfile } from '../types';
import { userProfileApi } from '../services/api';
import { logger } from '../logger';

const UserProfileForm: React.FC<{ onCreated?: () => void; welcome?: boolean }> = ({ onCreated, welcome }) => {
  const { t } = useTranslation();
  const [form] = Form.useForm();
  const [nameForm] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [profile, setProfile] = useState<UserProfile | null>(null);
  const [step, setStep] = useState(1);
  const [enteredName, setEnteredName] = useState('');
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    // In welcome mode, App.tsx already confirmed no profile exists — skip redundant fetch.
    if (welcome) {
      setProfile(null);
      setLoading(false);
      return;
    }
    loadProfile();
  }, []);

  const loadProfile = async () => {
    setLoading(true);
    try {
      const response = await userProfileApi.get();
      if (response.data.id) {
        setProfile(response.data);
        form.setFieldsValue(response.data);
        setDirty(false);
      } else {
        setProfile(null);
      }
    } catch (error) {
      logger.error('Failed to load user profile:', error);
      message.error(t('userProfile.loadError'));
    } finally {
      setLoading(false);
    }
  };

  const handleSave = async (name: string, bio?: string) => {
    setSaving(true);
    try {
      const response = await userProfileApi.upsert({ name, bio: bio || '' });
      const data = response.data;
      if (data.id) {
        setProfile(data);
        setDirty(false);
        onCreated?.();
        message.success(t('userProfile.saveSuccess'));
      } else {
        message.error((data as { detail?: string }).detail || t('userProfile.saveError'));
      }
    } catch (error) {
      logger.error('Failed to save user profile:', error);
      const detail = (error as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
      if (detail) {
        message.error(detail);
      } else {
        message.error(t('userProfile.saveError'));
      }
    } finally {
      setSaving(false);
    }
  };

  const handleNext = (values: { name: string }) => {
    setEnteredName(values.name);
    form.setFieldsValue({ name: values.name, bio: '' });
    setStep(2);
  };

  const handleFinish = async (values: { bio?: string }) => {
    await handleSave(enteredName, values.bio);
  };

  const handleSkip = async () => {
    await handleSave(enteredName);
  };

  if (loading) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', padding: '40px' }}>
        <Spin />
      </div>
    );
  }

  const isCreated = profile !== null;

  // Two-step onboarding for welcome (first-time) mode
  if (welcome && !isCreated) {
    return (
      <div style={{ position: 'relative', overflow: 'hidden', minHeight: 320 }}>
        {/* Step 1: name input */}
        <div style={{
          transition: 'opacity 0.35s ease, transform 0.35s ease',
          opacity: step === 1 ? 1 : 0,
          transform: step === 1 ? 'translateX(0)' : 'translateX(-24px)',
          pointerEvents: step === 1 ? 'auto' : 'none',
          position: step === 1 ? 'relative' : 'absolute',
          width: '100%',
        }}>
          <img
            src="./favicon.png"
            alt="logo"
            style={{ width: 48, height: 48, marginBottom: 32, opacity: 0.85 }}
          />
          <h1 style={{ fontSize: 26, fontWeight: 600, marginBottom: 12, color: 'var(--color-text-primary)' }}>
            {t('userProfile.welcome_1')}
          </h1>
          <p style={{ fontSize: 15, color: 'var(--color-text-secondary)', marginBottom: 36, lineHeight: 1.6 }}>
            {t('userProfile.welcome_2')}
          </p>
          <Form
            form={nameForm}
            layout="vertical"
            onFinish={handleNext}
          >
            <Form.Item
              name="name"
              rules={[{ required: true, message: t('userProfile.namePlaceholder') }]}
            >
              <Input
                size="large"
                placeholder={t('userProfile.namePlaceholder')}
                style={{ textAlign: 'center', fontSize: 18, height: 48 }}
              />
            </Form.Item>
            <p style={{ fontSize: 13, color: 'var(--color-text-placeholder)', textAlign: 'center', marginTop: -8, marginBottom: 24 }}>
              {t('userProfile.welcome_nameHint')}
            </p>
            <Form.Item>
              <Button
                type="primary"
                htmlType="submit"
                size="large"
                style={{ width: '100%' }}
                icon={<ArrowRightOutlined />}
                iconPosition="end"
              >
                {t('userProfile.welcome_next')}
              </Button>
            </Form.Item>
          </Form>
        </div>

        {/* Step 2: bio + confirm */}
        <div style={{
          transition: 'opacity 0.35s ease, transform 0.35s ease',
          opacity: step === 2 ? 1 : 0,
          transform: step === 2 ? 'translateX(0)' : 'translateX(24px)',
          pointerEvents: step === 2 ? 'auto' : 'none',
          position: step === 2 ? 'relative' : 'absolute',
          width: '100%',
          top: 0,
        }}>
          <div style={{ textAlign: 'left', marginBottom: 20 }}>
            <Button
              type="text"
              icon={<ArrowLeftOutlined />}
              onClick={() => { setStep(1); }}
              style={{ color: 'var(--color-text-placeholder)', paddingLeft: 0 }}
            >
              {t('common.back')}
            </Button>
          </div>
          <p style={{
            fontSize: 22, fontWeight: 600, color: 'var(--color-text-primary)',
            textAlign: 'center', marginBottom: 4,
          }}>
            {t('userProfile.welcome_step2_1')}，{enteredName}
          </p>
          <p style={{
            fontSize: 14, color: 'var(--color-text-secondary)',
            textAlign: 'center', marginBottom: 28, lineHeight: 1.6,
          }}>
            {t('userProfile.welcome_step2_2')}
          </p>
          <Form
            form={form}
            layout="vertical"
            onFinish={handleFinish}
          >
            <Form.Item name="bio">
              <Input.TextArea
                rows={3}
                placeholder={t('userProfile.bioPlaceholder')}
                style={{ resize: 'none' }}
              />
            </Form.Item>
            <Form.Item style={{ marginBottom: 12 }}>
              <Button
                type="primary"
                htmlType="submit"
                size="large"
                loading={saving}
                style={{ width: '100%' }}
              >
                {t('userProfile.welcome_start')}
              </Button>
            </Form.Item>
            <div style={{ textAlign: 'center' }}>
              <Button
                type="link"
                onClick={handleSkip}
                loading={saving}
                style={{ color: 'var(--color-text-placeholder)' }}
              >
                {t('userProfile.welcome_skip')}
              </Button>
            </div>
          </Form>
        </div>
      </div>
    );
  }

  // Settings panel mode (single form)
  return (
    <div className={welcome ? undefined : 'config-form-container'}>
      <Form
        form={form}
        layout="vertical"
        onFinish={(values) => handleSave(profile?.name || values.name, values.bio)}
        initialValues={profile ? profile : { name: '', bio: '' }}
        onValuesChange={() => setDirty(true)}
      >
        {isCreated ? (
          <div style={{ marginBottom: 24 }}>
            <div style={{ fontSize: 14, color: 'var(--color-text-secondary)', marginBottom: 4 }}>
              {t('userProfile.name')}
            </div>
            <div style={{ fontSize: 15, fontWeight: 500 }}>
              {profile?.name}
            </div>
            <div style={{ fontSize: 12, color: 'var(--color-text-placeholder)', marginTop: 2 }}>
              {t('userProfile.nameImmutable')}
            </div>
          </div>
        ) : (
          <Form.Item
            name="name"
            label={t('userProfile.name')}
            rules={[{ required: true, message: t('userProfile.namePlaceholder') }]}
          >
            <Input placeholder={t('userProfile.namePlaceholder')} />
          </Form.Item>
        )}

        <Form.Item name="bio" label={t('userProfile.bio')}>
          <Input.TextArea
            rows={3}
            placeholder={t('userProfile.bioPlaceholder')}
          />
        </Form.Item>

        <Form.Item style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 0 }}>
          <Button
            type="primary"
            htmlType="submit"
            icon={<SaveOutlined />}
            loading={saving}
            disabled={!dirty}
          >
            {t('common.save')}
          </Button>
        </Form.Item>
      </Form>
    </div>
  );
};

export default UserProfileForm;
