import React, { useEffect, useState } from 'react';

import { getFooterHTML, getSystemName } from '../helpers';
import { Layout, Tooltip } from '@douyinfe/semi-ui';

const Footer = () => {
  const systemName = getSystemName();
  const [footer, setFooter] = useState(getFooterHTML());
  let remainCheckTimes = 5;

  const loadFooter = () => {
    let footer_html = localStorage.getItem('footer_html');
    if (footer_html) {
      setFooter(footer_html);
    }
  };

  const defaultFooter = (
    <div className='custom-footer'>
      <a
        href='https://github.com/wisdgod/my-api'
        target='_blank'
        rel='noreferrer'
      >
        My API {import.meta.env.VITE_REACT_APP_VERSION}{' '}
      </a>
      由{' '}
      <a
        href='https://github.com/wisdgod'
        target='_blank'
        rel='noreferrer'
      >
        wisdgod
      </a>{' '}
      开发，基于{' '}
      <a
        href='https://github.com/songquanpeng/one-api'
        target='_blank'
        rel='noreferrer'
      >
        One API v0.5.4
      </a>{' '}
      与{' '}
      <a href="https://github.com/Calcium-Ion/new-api" target="_blank" rel="noreferrer">
        New API v0.2.4.0-alpha.4
      </a>
    </div>
  );

  useEffect(() => {
    const timer = setInterval(() => {
      if (remainCheckTimes <= 0) {
        clearInterval(timer);
        return;
      }
      remainCheckTimes--;
      loadFooter();
    }, 200);
    return () => clearTimeout(timer);
  }, []);

  return (
    <Layout>
      <Layout.Content style={{ textAlign: 'center' }}>
        {footer ? (
          <Tooltip content={defaultFooter}>
            <div
              className='custom-footer'
              dangerouslySetInnerHTML={{ __html: footer }}
            ></div>
          </Tooltip>
        ) : (
          defaultFooter
        )}
      </Layout.Content>
    </Layout>
  );
};

export default Footer;
